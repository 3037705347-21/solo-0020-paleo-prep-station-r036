package station

import (
	"context"
	"time"

	"example.com/paleo-prep-station/internal/contracts"
	"example.com/paleo-prep-station/internal/domain"
	"example.com/paleo-prep-station/internal/repository"
)

func (s *Service) CreatePlan(
	ctx context.Context,
	specimenID string,
	request contracts.CreatePlanRequest,
) (domain.PrepPlan, error) {
	if err := ctx.Err(); err != nil {
		return domain.PrepPlan{}, err
	}

	var created domain.PrepPlan
	err := s.repo.Mutate(func(m *repository.Mutation) error {
		specimen, ok := m.GetSpecimen(specimenID)
		if !ok {
			return notFoundSpecimen(specimenID)
		}
		if m.HasOpenPlan(specimenID) {
			return contracts.Conflict("open_plan_exists", "该标本已存在未结束的修复方案")
		}
		plan, err := domain.NewPrepPlan(
			s.nextID("plan"),
			specimen,
			request.Objective,
			request.Method,
			request.Adhesive,
			request.TargetExposure,
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := specimen.MarkPlanned(s.now()); err != nil {
			return err
		}
		m.PutPlan(plan)
		m.PutSpecimen(specimen)
		created = plan
		return nil
	})
	return created, err
}

func (s *Service) ApprovePlan(
	ctx context.Context,
	planID string,
	request contracts.ApprovalRequest,
) (domain.PrepPlan, error) {
	if err := ctx.Err(); err != nil {
		return domain.PrepPlan{}, err
	}

	var approved domain.PrepPlan
	err := s.repo.Mutate(func(m *repository.Mutation) error {
		plan, err := loadOnlinePlanForWrite(m, planID)
		if err != nil {
			return err
		}
		specimen, ok := m.GetSpecimen(plan.SpecimenID)
		if !ok {
			return notFoundSpecimen(plan.SpecimenID)
		}
		if err := plan.Approve(request.ApprovedBy, s.now()); err != nil {
			return err
		}
		if err := specimen.MarkPreparing(s.now()); err != nil {
			return err
		}
		m.PutPlan(plan)
		m.PutSpecimen(specimen)
		approved = plan
		return nil
	})
	return approved, err
}

func (s *Service) CancelPlan(
	ctx context.Context,
	planID string,
	request contracts.CancelPlanRequest,
) (domain.PrepPlan, error) {
	if err := ctx.Err(); err != nil {
		return domain.PrepPlan{}, err
	}

	var cancelled domain.PrepPlan
	err := s.repo.Mutate(func(m *repository.Mutation) error {
		plan, err := loadOnlinePlanForWrite(m, planID)
		if err != nil {
			return err
		}
		// Cancellation never moves the specimen; it only closes the plan. The
		// specimen must still exist so the reference chain stays complete.
		if _, ok := m.GetSpecimen(plan.SpecimenID); !ok {
			return notFoundSpecimen(plan.SpecimenID)
		}
		if err := plan.Cancel(request.CancelledBy, s.now()); err != nil {
			return err
		}
		m.PutPlan(plan)
		cancelled = plan
		return nil
	})
	return cancelled, err
}

func (s *Service) GetPlan(ctx context.Context, planID string) (PlanSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return PlanSnapshot{}, err
	}
	var snapshot PlanSnapshot
	err := s.repo.Read(func(m *repository.Mutation) error {
		plan, err := loadOnlinePlan(m, planID)
		if err != nil {
			return err
		}
		snapshot = PlanSnapshot{
			Plan:     plan,
			Progress: m.ProgressForPlan(planID),
		}
		return nil
	})
	return snapshot, err
}

// ArchivedPlanRef is the locator left in a specimen's plan listing when one of
// its plans left the online collection. It never carries the sealed content,
// only enough to locate and verify the immutable archive.
type ArchivedPlanRef struct {
	PlanID     string    `json:"plan_id"`
	ArchiveID  string    `json:"archive_id"`
	Status     string    `json:"status"`
	ArchivedAt time.Time `json:"archived_at"`
	Digest     string    `json:"digest"`
}

// PlanListing keeps the online plan items exactly as before and additionally
// discloses which plans were archived and where their existence facts live.
type PlanListing struct {
	Items    []domain.PrepPlan `json:"items"`
	Archived []ArchivedPlanRef `json:"archived"`
}

func (s *Service) ListPlansForSpecimen(
	ctx context.Context,
	specimenID string,
) (PlanListing, error) {
	if err := ctx.Err(); err != nil {
		return PlanListing{}, err
	}
	var listing PlanListing
	err := s.repo.Read(func(m *repository.Mutation) error {
		if _, ok := m.GetSpecimen(specimenID); !ok {
			return notFoundSpecimen(specimenID)
		}
		// Only the online collection is returned as items; sealed plans are
		// disclosed through explicit, locatable archive references instead of
		// vanishing silently.
		listing.Items = m.ListPlansForSpecimen(specimenID)
		for _, archive := range m.ListArchivesForSpecimen(specimenID) {
			listing.Archived = append(listing.Archived, ArchivedPlanRef{
				PlanID:     archive.PlanID,
				ArchiveID:  archive.ID,
				Status:     string(archive.Plan.Status),
				ArchivedAt: archive.ArchivedAt,
				Digest:     archive.Digest,
			})
		}
		return nil
	})
	return listing, err
}

// loadOnlinePlan fetches a plan for an online read/write and converts an
// archived id into an explicit, locatable error instead of a silent miss.
func loadOnlinePlan(m *repository.Mutation, planID string) (domain.PrepPlan, error) {
	plan, ok := m.GetPlan(planID)
	if ok {
		return plan, nil
	}
	if archive, archived := m.GetArchiveForPlan(planID); archived {
		return domain.PrepPlan{}, archivedPlanRead(planID, archive.ID)
	}
	return domain.PrepPlan{}, notFoundPlan(planID)
}

// loadOnlinePlanForWrite is the write-side counterpart: submissions against a
// sealed plan are rejected as conflicts even though its archive remains
// readable.
func loadOnlinePlanForWrite(m *repository.Mutation, planID string) (domain.PrepPlan, error) {
	plan, ok := m.GetPlan(planID)
	if ok {
		return plan, nil
	}
	if archive, archived := m.GetArchiveForPlan(planID); archived {
		return domain.PrepPlan{}, archivedPlanWrite(planID, archive.ID)
	}
	return domain.PrepPlan{}, notFoundPlan(planID)
}
