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

// defaultArchiveRetention is how long a finished plan stays inside the recent
// query window. Plans younger than this still participate in everyday reads
// and cannot be archived.
const defaultArchiveRetention = 30 * 24 * time.Hour

type Service struct {
	repo             *repository.MemoryRepository
	now              func() time.Time
	seq              atomic.Uint64
	archiveRetention time.Duration
}

// Option customizes the service, primarily for deterministic local checks.
type Option func(*Service)

// WithClock overrides the time source used by every state change.
func WithClock(clock func() time.Time) Option {
	return func(s *Service) {
		if clock != nil {
			s.now = clock
		}
	}
}

// WithArchiveRetention overrides the recent-query window. A non-positive value
// means terminal plans may leave the online set immediately.
func WithArchiveRetention(retention time.Duration) Option {
	return func(s *Service) {
		s.archiveRetention = retention
	}
}

func New(repo *repository.MemoryRepository, options ...Option) *Service {
	if repo == nil {
		repo = repository.NewMemory()
	}
	service := &Service{
		repo:             repo,
		now:              func() time.Time { return time.Now().UTC() },
		archiveRetention: defaultArchiveRetention,
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

// loadOnlinePlan locates a plan that is still part of the online set. When the
// plan has been archived it fails with a locatable reference instead of a
// plain missing error, so reads are never silently retargeted to the archive.
func (s *Service) loadOnlinePlan(m *repository.Mutation, planID string) (domain.PrepPlan, error) {
	if archived, ok := m.GetArchiveForPlan(planID); ok {
		return domain.PrepPlan{}, archivedPlanError(planID, archived.ID)
	}
	plan, ok := m.GetPlan(planID)
	if !ok {
		return domain.PrepPlan{}, notFoundPlan(planID)
	}
	return plan, nil
}
