package station

import (
	"context"
	"errors"
	"testing"
	"time"

	"example.com/paleo-prep-station/internal/contracts"
	"example.com/paleo-prep-station/internal/domain"
	"example.com/paleo-prep-station/internal/repository"
)

type fixture struct {
	service  *Service
	repo     *repository.MemoryRepository
	planID   string
	specimen string
}

func releasedFixture(t *testing.T, retention time.Duration, now time.Time) fixture {
	t.Helper()
	ctx := context.Background()
	repo := repository.NewMemory()
	service := New(repo, WithArchiveRetention(retention), WithClock(func() time.Time { return now }))

	specimen, err := service.RegisterSpecimen(ctx, contracts.RegisterSpecimenRequest{
		CatalogNumber: "PPS-ARCH-1",
		FieldLabel:    "field",
		MatrixKind:    "泥岩",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	plan, err := service.CreatePlan(ctx, specimen.ID, contracts.CreatePlanRequest{
		Objective:      "暴露主要关节面",
		Method:         "显微剥离",
		TargetExposure: 80,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if _, err := service.ApprovePlan(ctx, plan.ID, contracts.ApprovalRequest{ApprovedBy: "curator-lin"}); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, err := service.LogProgress(ctx, plan.ID, contracts.ProgressRequest{
		Stage: "加固", Percent: 100, RecordedBy: "chen",
	}); err != nil {
		t.Fatalf("progress: %v", err)
	}
	if _, err := service.ReleaseSpecimen(ctx, specimen.ID, contracts.ReleaseRequest{
		Purpose: "研究", Recipient: "研究组", ApprovedBy: "keeper-zhou",
	}); err != nil {
		t.Fatalf("release: %v", err)
	}
	return fixture{service: service, repo: repo, planID: plan.ID, specimen: specimen.ID}
}

func TestArchivePlan_MovesPlanOutOfOnlineScopeAtomically(t *testing.T) {
	now := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	fx := releasedFixture(t, 0, now)
	ctx := context.Background()

	before, err := fx.service.GetPlan(ctx, fx.planID)
	if err != nil {
		t.Fatalf("read before archive: %v", err)
	}
	if before.Plan.Status != domain.PlanCompleted || len(before.Progress) != 1 {
		t.Fatalf("unexpected pre-archive snapshot: %+v", before)
	}

	archive, err := fx.service.ArchivePlan(ctx, fx.planID, contracts.ArchivePlanRequest{ArchivedBy: "registrar-gao"})
	if err != nil {
		t.Fatalf("archive: %v", err)
	}

	// Online read must fail explicitly and locate the archive, never return a
	// generic not-found or an empty snapshot.
	_, err = fx.service.GetPlan(ctx, fx.planID)
	assertCode(t, err, contracts.KindNotFound, "plan_archived", archive.ID)

	// Writes against the sealed plan are conflicts.
	_, err = fx.service.LogProgress(ctx, fx.planID, contracts.ProgressRequest{
		Stage: "补记", Percent: 100, RecordedBy: "chen",
	})
	assertCode(t, err, contracts.KindConflict, "plan_archived", archive.ID)

	// The archive is locatable and verifies.
	fetched, err := fx.service.GetArchive(ctx, archive.ID)
	if err != nil {
		t.Fatalf("get archive: %v", err)
	}
	if fetched.Digest != archive.Digest || fetched.PlanID != fx.planID {
		t.Fatalf("archive fact mismatch: %+v", fetched)
	}
	if err := fetched.Verify(); err != nil {
		t.Fatalf("stored archive must verify: %v", err)
	}

	// The specimen itself was not silently rewritten by sealing.
	specimen, err := fx.service.GetSpecimen(ctx, fx.specimen)
	if err != nil {
		t.Fatalf("get specimen: %v", err)
	}
	if specimen.Status != domain.SpecimenAvailable {
		t.Fatalf("specimen status changed during archive: %s", specimen.Status)
	}

	// The listing discloses the archive instead of silently dropping the plan.
	listing, err := fx.service.ListPlansForSpecimen(ctx, fx.specimen)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listing.Items) != 0 {
		t.Fatalf("sealed plan must leave online items: %+v", listing.Items)
	}
	if len(listing.Archived) != 1 || listing.Archived[0].ArchiveID != archive.ID ||
		listing.Archived[0].PlanID != fx.planID {
		t.Fatalf("listing must locate the archive: %+v", listing.Archived)
	}
}

func TestArchivePlan_RejectsDuplicateWithoutSecondArchive(t *testing.T) {
	now := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	fx := releasedFixture(t, 0, now)
	ctx := context.Background()

	first, err := fx.service.ArchivePlan(ctx, fx.planID, contracts.ArchivePlanRequest{ArchivedBy: "registrar-gao"})
	if err != nil {
		t.Fatalf("first archive: %v", err)
	}
	_, err = fx.service.ArchivePlan(ctx, fx.planID, contracts.ArchivePlanRequest{ArchivedBy: "registrar-gao"})
	assertCode(t, err, contracts.KindConflict, "plan_already_archived", first.ID)

	// The second failure must point at exactly the first archive.
	appErr, _ := contracts.AsError(err)
	if appErr.Details["archive_id"] != first.ID {
		t.Fatalf("duplicate error must locate the existing archive, got %v", appErr.Details)
	}
	got, err := fx.service.GetArchive(ctx, first.ID)
	if err != nil {
		t.Fatalf("first archive must remain the single valid result: %v", err)
	}
	if got.ID != first.ID {
		t.Fatalf("archive identity changed: %s", got.ID)
	}
}

func TestArchivePlan_FailedSealChangesNothing(t *testing.T) {
	now := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	// A 24h retention window blocks the archive of a just-closed plan.
	fx := releasedFixture(t, 24*time.Hour, now)
	ctx := context.Background()

	if _, err := fx.service.ArchivePlan(ctx, fx.planID, contracts.ArchivePlanRequest{ArchivedBy: "registrar-gao"}); err == nil {
		t.Fatal("archive within retention window must fail")
	}

	// The plan, its progress and the specimen remain exactly where they were.
	snapshot, err := fx.service.GetPlan(ctx, fx.planID)
	if err != nil {
		t.Fatalf("blocked archive must keep the plan readable: %v", err)
	}
	if snapshot.Plan.Status != domain.PlanCompleted || len(snapshot.Progress) != 1 {
		t.Fatalf("blocked archive rewrote plan state: %+v", snapshot)
	}
	specimen, err := fx.service.GetSpecimen(ctx, fx.specimen)
	if err != nil {
		t.Fatalf("get specimen: %v", err)
	}
	if specimen.Status != domain.SpecimenAvailable {
		t.Fatalf("blocked archive rewrote specimen: %s", specimen.Status)
	}
	listing, err := fx.service.ListPlansForSpecimen(ctx, fx.specimen)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listing.Items) != 1 || len(listing.Archived) != 0 {
		t.Fatalf("blocked archive altered listing scope: %+v", listing)
	}
}

func TestArchivePlan_UnknownPlanIsNotFound(t *testing.T) {
	now := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	fx := releasedFixture(t, 0, now)
	ctx := context.Background()

	if _, err := fx.service.ArchivePlan(ctx, "plan-missing", contracts.ArchivePlanRequest{ArchivedBy: "registrar-gao"}); err == nil {
		t.Fatal("unknown plan must not archive")
	} else if appErr, ok := contracts.AsError(err); !ok || appErr.Kind != contracts.KindNotFound {
		t.Fatalf("expected not_found, got %v", err)
	}
	if _, err := fx.service.GetArchive(ctx, "archive-missing"); err == nil {
		t.Fatal("unknown archive must be not found")
	} else if appErr, ok := contracts.AsError(err); !ok || appErr.Kind != contracts.KindNotFound {
		t.Fatalf("expected not_found, got %v", err)
	}
}

func assertCode(t *testing.T, err error, kind contracts.ErrorKind, code, archiveID string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s/%s, got nil", kind, code)
	}
	appErr, ok := contracts.AsError(err)
	if !ok {
		t.Fatalf("expected contracts error, got %T: %v", err, err)
	}
	if appErr.Kind != kind || appErr.Code != code {
		t.Fatalf("expected %s/%s, got %s/%s", kind, code, appErr.Kind, appErr.Code)
	}
	if archiveID != "" && appErr.Details["archive_id"] != archiveID {
		t.Fatalf("expected archive_id detail %q, got %v", archiveID, appErr.Details)
	}
}

func TestSealPlan_RacesToSingleArchive(t *testing.T) {
	now := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	fx := releasedFixture(t, 0, now)
	ctx := context.Background()

	// Two concurrent submissions: exactly one may seal.
	type result struct {
		id  string
		err error
	}
	results := make(chan result, 2)
	barrier := make(chan struct{})
	for i := 0; i < 2; i++ {
		go func() {
			<-barrier
			archive, err := fx.service.ArchivePlan(ctx, fx.planID, contracts.ArchivePlanRequest{ArchivedBy: "registrar-gao"})
			results <- result{id: archive.ID, err: err}
		}()
	}
	close(barrier)

	var successCount, conflictCount int
	var winnerID string
	for i := 0; i < 2; i++ {
		r := <-results
		switch {
		case r.err == nil:
			successCount++
			winnerID = r.id
		default:
			var appErr *contracts.Error
			if errors.As(r.err, &appErr) && appErr.Code == "plan_already_archived" {
				conflictCount++
			}
		}
	}
	if successCount != 1 || conflictCount != 1 {
		t.Fatalf("expected exactly one success and one duplicate conflict, got %d/%d", successCount, conflictCount)
	}
	if winnerID == "" {
		t.Fatal("no winning archive id")
	}
}
