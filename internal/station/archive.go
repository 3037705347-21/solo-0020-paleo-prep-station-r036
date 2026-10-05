package station

import (
	"context"
	"errors"

	"example.com/paleo-prep-station/internal/contracts"
	"example.com/paleo-prep-station/internal/domain"
	"example.com/paleo-prep-station/internal/repository"
)

// ArchivePlan seals a finished plan into an immutable archive and removes it
// from the online collection. Every eligibility rule and the sealing commit
// run inside the same repository mutation, and a plan that already has an
// archive is refused instead of producing a second result.
func (s *Service) ArchivePlan(
	ctx context.Context,
	planID string,
	request contracts.ArchivePlanRequest,
) (domain.PlanArchive, error) {
	if err := ctx.Err(); err != nil {
		return domain.PlanArchive{}, err
	}

	var archived domain.PlanArchive
	err := s.repo.Mutate(func(m *repository.Mutation) error {
		// A repeat submission must find the existing archive and fail against
		// it; it can never mint a second valid result.
		if existing, ok := m.GetArchiveForPlan(planID); ok {
			return alreadyArchived(existing.ID)
		}

		plan, ok := m.GetPlan(planID)
		if !ok {
			return notFoundPlan(planID)
		}
		specimen, ok := m.GetSpecimen(plan.SpecimenID)
		if !ok {
			return notFoundSpecimen(plan.SpecimenID)
		}
		entries := m.ProgressForPlan(planID)

		var release *domain.ReleaseRecord
		if record, exists := m.ReleaseForSpecimen(specimen.ID); exists {
			release = &record
		}

		archive, err := domain.NewPlanArchive(
			s.nextID("archive"),
			plan,
			entries,
			specimen,
			release,
			request.ArchivedBy,
			s.archiveRetention,
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := archive.Verify(); err != nil {
			return err
		}
		if err := m.SealPlan(archive); err != nil {
			return mapSealError(planID, err)
		}
		archived = archive
		return nil
	})
	return archived, err
}

// GetArchive locates an immutable archive by its id and verifies its digest.
// The archive stays readable forever even though its plan is no longer in the
// online collection.
func (s *Service) GetArchive(ctx context.Context, archiveID string) (domain.PlanArchive, error) {
	if err := ctx.Err(); err != nil {
		return domain.PlanArchive{}, err
	}
	var archive domain.PlanArchive
	err := s.repo.Read(func(m *repository.Mutation) error {
		found, ok := m.GetArchive(archiveID)
		if !ok {
			return notFoundArchive(archiveID)
		}
		if err := found.Verify(); err != nil {
			return err
		}
		archive = found
		return nil
	})
	return archive, err
}

func mapSealError(planID string, err error) error {
	switch {
	case errors.Is(err, repository.ErrPlanAlreadyArchived):
		return contracts.Conflict(
			"plan_already_archived",
			"修复方案已归档，重复归档不会生成第二份结果",
		)
	case errors.Is(err, repository.ErrPlanMissing):
		return notFoundPlan(planID)
	case errors.Is(err, repository.ErrArchiveIDExists):
		return contracts.Internal("archive_id_collision", "归档标识冲突，请重试", err)
	default:
		return contracts.Internal("archive_seal_failed", "归档提交失败", err)
	}
}
