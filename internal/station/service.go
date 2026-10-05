package station

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync/atomic"
	"time"

	"example.com/paleo-prep-station/internal/domain"
	"example.com/paleo-prep-station/internal/repository"
)

// DefaultArchiveRetention is how long a finished plan stays in the recent
// query scope before it may leave the online collection.
const DefaultArchiveRetention = 30 * 24 * time.Hour

type Service struct {
	repo             *repository.MemoryRepository
	now              func() time.Time
	archiveRetention time.Duration
	seq              atomic.Uint64
}

type Option func(*Service)

// WithArchiveRetention overrides how long finished plans remain in the recent
// query scope before they can be archived. Negative values collapse to zero.
func WithArchiveRetention(window time.Duration) Option {
	return func(s *Service) {
		if window < 0 {
			window = 0
		}
		s.archiveRetention = window
	}
}

// WithClock overrides the time source. It exists for deterministic local
// workflow checks and tests.
func WithClock(clock func() time.Time) Option {
	return func(s *Service) {
		if clock != nil {
			s.now = clock
		}
	}
}

func New(repo *repository.MemoryRepository, options ...Option) *Service {
	if repo == nil {
		repo = repository.NewMemory()
	}
	service := &Service{
		repo:             repo,
		now:              func() time.Time { return time.Now().UTC() },
		archiveRetention: DefaultArchiveRetention,
	}
	for _, option := range options {
		option(service)
	}
	return service
}

func (s *Service) nextID(prefix string) string {
	var random [6]byte
	if _, err := rand.Read(random[:]); err == nil {
		return prefix + "-" + hex.EncodeToString(random[:])
	}
	local := s.seq.Add(1)
	return prefix + "-" + time.Now().UTC().Format("20060102150405") + "-" + hex.EncodeToString([]byte{byte(local)})
}

type PlanSnapshot struct {
	Plan     domain.PrepPlan        `json:"plan"`
	Progress []domain.ProgressEntry `json:"progress"`
}

func (s *Service) ListSpecimens(ctx context.Context) ([]domain.Specimen, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var values []domain.Specimen
	err := s.repo.Read(func(m *repository.Mutation) error {
		values = m.ListSpecimens()
		return nil
	})
	return values, err
}

func (s *Service) GetSpecimen(ctx context.Context, specimenID string) (domain.Specimen, error) {
	if err := ctx.Err(); err != nil {
		return domain.Specimen{}, err
	}
	var value domain.Specimen
	err := s.repo.Read(func(m *repository.Mutation) error {
		found, ok := m.GetSpecimen(specimenID)
		if !ok {
			return notFoundSpecimen(specimenID)
		}
		value = found
		return nil
	})
	return value, err
}
