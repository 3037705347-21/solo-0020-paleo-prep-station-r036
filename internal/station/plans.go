package station

import (
	"context"

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
		plan, err := s.loadOnlinePlan(m, planID)
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
		plan, err := s.loadOnlinePlan(m, planID)
		if err != nil {
			return err
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
		plan, err := s.loadOnlinePlan(m, planID)
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

func (s *Service) ListPlansForSpecimen(
	ctx context.Context,
	specimenID string,
) ([]domain.PrepPlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var values []domain.PrepPlan
	err := s.repo.Read(func(m *repository.Mutation) error {
		if _, ok := m.GetSpecimen(specimenID); !ok {
			return notFoundSpecimen(specimenID)
		}
		values = m.ListPlansForSpecimen(specimenID)
		return nil
	})
	return values, err
}

// ArchivePlanResult reports the created/returned archive together with whether
// this call actually performed the archival. Repeated submissions return the
// same archive with Created=false, so a second valid result can never exist.
type ArchivePlanResult struct {
	Archive domain.PlanArchive `json:"archive"`
	Created bool               `json:"created"`
}

func (s *Service) ArchivePlan(
	ctx context.Context,
	planID string,
	request contracts.ArchivePlanRequest,
) (ArchivePlanResult, error) {
	if err := ctx.Err(); err != nil {
		return ArchivePlanResult{}, err
	}

	var result ArchivePlanResult
	err := s.repo.Mutate(func(m *repository.Mutation) error {
		// Idempotency first: an archived plan never produces a second archive.
		if existing, ok := m.GetArchiveForPlan(planID); ok {
			if !existing.MatchesDigest() {
				return contracts.Internal("archive_tampered", "归档内容与摘要不一致，归档不可信", nil)
			}
			result = ArchivePlanResult{Archive: existing, Created: false}
			return nil
		}
		plan, ok := m.GetPlan(planID)
		if !ok {
			return notFoundPlan(planID)
		}
		if !plan.IsTerminal() {
			return contracts.Conflict("plan_not_archivable", "只有已完成或已取消方案可以归档")
		}
		if s.archiveRetention > 0 && s.now().Sub(plan.UpdatedAt) < s.archiveRetention {
			return contracts.Conflict("plan_within_retention_window", "方案仍在最近查询保留期内，不能归档")
		}

		progress := m.ProgressForPlan(planID)
		var releaseID string
		if plan.IsCompleted() {
			release, exists := m.ReleaseForSpecimen(plan.SpecimenID)
			if !exists {
				// The specimen is still waiting on release: an unsettled
				// release reference keeps the plan in the online set.
				return contracts.Conflict("release_unsettled", "存在未完成的研究放行，不能归档")
			}
			releaseID = release.ID
		}

		archive, err := domain.NewPlanArchive(
			s.nextID("archive"),
			plan,
			progress,
			releaseID,
			request.ArchivedBy,
			request.AuditReference,
			s.now(),
		)
		if err != nil {
			return err
		}
		m.PutArchive(archive)
		m.RemovePlan(planID)
		result = ArchivePlanResult{Archive: archive, Created: true}
		return nil
	})
	return result, err
}

func (s *Service) GetPlanArchive(ctx context.Context, planID string) (domain.PlanArchive, error) {
	if err := ctx.Err(); err != nil {
		return domain.PlanArchive{}, err
	}
	var value domain.PlanArchive
	err := s.repo.Read(func(m *repository.Mutation) error {
		archive, ok := m.GetArchiveForPlan(planID)
		if !ok {
			return notFoundArchive(planID)
		}
		if !archive.MatchesDigest() {
			return contracts.Internal("archive_tampered", "归档内容与摘要不一致，归档不可信", nil)
		}
		value = archive
		return nil
	})
	return value, err
}

// GetPlanArchiveByID locates an archive via its own identifier, supporting the
// reference handed out by archived online reads.
func (s *Service) GetPlanArchiveByID(ctx context.Context, archiveID string) (domain.PlanArchive, error) {
	if err := ctx.Err(); err != nil {
		return domain.PlanArchive{}, err
	}
	var value domain.PlanArchive
	err := s.repo.Read(func(m *repository.Mutation) error {
		archive, ok := m.GetArchiveByID(archiveID)
		if !ok {
			return contracts.NotFound("plan_archive_not_found", "未找到归档记录 "+archiveID)
		}
		if !archive.MatchesDigest() {
			return contracts.Internal("archive_tampered", "归档内容与摘要不一致，归档不可信", nil)
		}
		value = archive
		return nil
	})
	return value, err
}

func (s *Service) ListArchivesForSpecimen(
	ctx context.Context,
	specimenID string,
) ([]domain.PlanArchive, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var values []domain.PlanArchive
	err := s.repo.Read(func(m *repository.Mutation) error {
		if _, ok := m.GetSpecimen(specimenID); !ok {
			return notFoundSpecimen(specimenID)
		}
		values = m.ListArchivesForSpecimen(specimenID)
		for _, archive := range values {
			if !archive.MatchesDigest() {
				return contracts.Internal("archive_tampered", "归档内容与摘要不一致，归档不可信", nil)
			}
		}
		return nil
	})
	return values, err
}
