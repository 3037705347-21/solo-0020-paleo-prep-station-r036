package station

import (
	"context"
	"testing"
	"time"

	"example.com/paleo-prep-station/internal/contracts"
	"example.com/paleo-prep-station/internal/domain"
	"example.com/paleo-prep-station/internal/repository"
)

type archiveFixture struct {
	service *Service
	repo    *repository.MemoryRepository
	clock   *controlledClock
}

type controlledClock struct {
	current time.Time
}

func (c *controlledClock) now() time.Time { return c.current }
func (c *controlledClock) advance(d time.Duration) {
	c.current = c.current.Add(d)
}

func newArchiveFixture(t *testing.T, retention time.Duration) *archiveFixture {
	t.Helper()
	clock := &controlledClock{current: time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)}
	repo := repository.NewMemory()
	service := New(repo, WithClock(clock.now), WithArchiveRetention(retention))
	return &archiveFixture{service: service, repo: repo, clock: clock}
}

func (f *archiveFixture) registerSpecimen(t *testing.T, catalog string) domain.Specimen {
	t.Helper()
	specimen, err := f.service.RegisterSpecimen(context.Background(), contracts.RegisterSpecimenRequest{
		CatalogNumber: catalog,
		FieldLabel:    "北坡-07",
		MatrixKind:    "粉砂岩",
	})
	if err != nil {
		t.Fatalf("register specimen: %v", err)
	}
	return specimen
}

func (f *archiveFixture) createPlan(t *testing.T, specimenID string) domain.PrepPlan {
	t.Helper()
	plan, err := f.service.CreatePlan(context.Background(), specimenID, contracts.CreatePlanRequest{
		Objective:      "暴露主要关节面",
		Method:         "手工显微剥离",
		Adhesive:       "可逆树脂",
		TargetExposure: 80,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	return plan
}

func (f *archiveFixture) approvePlan(t *testing.T, planID string) {
	t.Helper()
	if _, err := f.service.ApprovePlan(context.Background(), planID, contracts.ApprovalRequest{ApprovedBy: "curator-lin"}); err != nil {
		t.Fatalf("approve plan: %v", err)
	}
}

func (f *archiveFixture) logProgress(t *testing.T, planID string, stage string, percent int) {
	t.Helper()
	if _, err := f.service.LogProgress(context.Background(), planID, contracts.ProgressRequest{
		Stage:      stage,
		Percent:    percent,
		RecordedBy: "preparator-chen",
	}); err != nil {
		t.Fatalf("log progress: %v", err)
	}
}

func (f *archiveFixture) completePlan(t *testing.T, specimenID, planID string) {
	t.Helper()
	f.approvePlan(t, planID)
	f.logProgress(t, planID, "矩阵剥离", 40)
	f.logProgress(t, planID, "表面加固", 100)
}

func (f *archiveFixture) releaseSpecimen(t *testing.T, specimenID string) domain.ReleaseRecord {
	t.Helper()
	record, err := f.service.ReleaseSpecimen(context.Background(), specimenID, contracts.ReleaseRequest{
		Purpose:    "比较解剖研究",
		Recipient:  "地层研究组",
		ApprovedBy: "keeper-zhou",
	})
	if err != nil {
		t.Fatalf("release specimen: %v", err)
	}
	return record
}

func archiveRequest() contracts.ArchivePlanRequest {
	return contracts.ArchivePlanRequest{ArchivedBy: "keeper-zhou", AuditReference: "AUDIT-2026-0014"}
}

func assertCode(t *testing.T, err error, wantCode string) {
	t.Helper()
	appErr, ok := contracts.AsError(err)
	if !ok {
		t.Fatalf("expected contracts error %q, got %v", wantCode, err)
	}
	if appErr.Code != wantCode {
		t.Fatalf("expected code %q, got %q (%v)", wantCode, appErr.Code, err)
	}
}

func TestArchiveCompletedPlanHappyPath(t *testing.T) {
	f := newArchiveFixture(t, 0)
	specimen := f.registerSpecimen(t, "PPS-2026-101")
	plan := f.createPlan(t, specimen.ID)
	f.completePlan(t, specimen.ID, plan.ID)
	release := f.releaseSpecimen(t, specimen.ID)

	result, err := f.service.ArchivePlan(context.Background(), plan.ID, archiveRequest())
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if !result.Created {
		t.Fatal("first archival must report created=true")
	}
	archive := result.Archive
	if archive.PlanID != plan.ID || archive.ReleaseID != release.ID {
		t.Fatalf("archive references wrong: %+v", archive)
	}
	if len(archive.Progress) != 2 || !archive.MatchesDigest() {
		t.Fatal("archive must preserve both progress entries and a valid digest")
	}

	// The plan leaves the online set and cannot be read there anymore.
	if _, err := f.service.GetPlan(context.Background(), plan.ID); err == nil {
		t.Fatal("archived plan must not be readable through the online snapshot")
	} else {
		appErr, ok := contracts.AsError(err)
		if !ok || appErr.Code != "plan_archived" || appErr.Reference != "/plan-archives/"+archive.ID {
			t.Fatalf("expected locatable plan_archived error, got %v", err)
		}
	}

	// The surviving fact is locatable both by plan id and by archive id.
	byPlan, err := f.service.GetPlanArchive(context.Background(), plan.ID)
	if err != nil {
		t.Fatalf("get archive by plan id: %v", err)
	}
	byID, err := f.service.GetPlanArchiveByID(context.Background(), archive.ID)
	if err != nil {
		t.Fatalf("get archive by id: %v", err)
	}
	if byPlan.ID != archive.ID || byID.ID != archive.ID {
		t.Fatal("archive locators disagree")
	}

	// Online listing drops the plan; the specimen's archive listing keeps it.
	plans, err := f.service.ListPlansForSpecimen(context.Background(), specimen.ID)
	if err != nil || len(plans) != 0 {
		t.Fatalf("online plans after archival = %v, err=%v", plans, err)
	}
	archives, err := f.service.ListArchivesForSpecimen(context.Background(), specimen.ID)
	if err != nil || len(archives) != 1 || archives[0].ID != archive.ID {
		t.Fatalf("archives listing = %v, err=%v", archives, err)
	}

	// Pre-existing state is not silently rewritten: specimen stays available.
	stored, err := f.service.GetSpecimen(context.Background(), specimen.ID)
	if err != nil {
		t.Fatalf("get specimen: %v", err)
	}
	if stored.Status != domain.SpecimenAvailable {
		t.Fatalf("specimen status changed by archival: %s", stored.Status)
	}
}

func TestArchivePlanIsIdempotent(t *testing.T) {
	f := newArchiveFixture(t, 0)
	specimen := f.registerSpecimen(t, "PPS-2026-102")
	plan := f.createPlan(t, specimen.ID)
	f.completePlan(t, specimen.ID, plan.ID)
	f.releaseSpecimen(t, specimen.ID)

	first, err := f.service.ArchivePlan(context.Background(), plan.ID, archiveRequest())
	if err != nil {
		t.Fatalf("first archive: %v", err)
	}
	second, err := f.service.ArchivePlan(context.Background(), plan.ID, contracts.ArchivePlanRequest{
		ArchivedBy:     "someone-else",
		AuditReference: "AUDIT-2026-9999",
	})
	if err != nil {
		t.Fatalf("repeat archive must succeed idempotently: %v", err)
	}
	if second.Created {
		t.Fatal("repeat archival must report created=false")
	}
	if second.Archive.ID != first.Archive.ID ||
		second.Archive.ContentDigest != first.Archive.ContentDigest ||
		second.Archive.AuditReference != "AUDIT-2026-0014" {
		t.Fatal("repeat archival must return the single original archive unchanged")
	}

	archives, err := f.service.ListArchivesForSpecimen(context.Background(), specimen.ID)
	if err != nil || len(archives) != 1 {
		t.Fatalf("exactly one archive must exist, got %d (err=%v)", len(archives), err)
	}
}

func TestArchiveBlockedInsideRetentionWindow(t *testing.T) {
	f := newArchiveFixture(t, 30*24*time.Hour)
	specimen := f.registerSpecimen(t, "PPS-2026-103")
	plan := f.createPlan(t, specimen.ID)
	f.completePlan(t, specimen.ID, plan.ID)
	f.releaseSpecimen(t, specimen.ID)

	_, err := f.service.ArchivePlan(context.Background(), plan.ID, archiveRequest())
	assertCode(t, err, "plan_within_retention_window")

	// Still readable online after the rejected submission.
	snapshot, err := f.service.GetPlan(context.Background(), plan.ID)
	if err != nil || snapshot.Plan.Status != domain.PlanCompleted {
		t.Fatalf("rejected archival must leave the online plan intact: %v", err)
	}

	f.clock.advance(30*24*time.Hour + time.Minute)
	if _, err := f.service.ArchivePlan(context.Background(), plan.ID, archiveRequest()); err != nil {
		t.Fatalf("archive after retention window: %v", err)
	}
}

func TestArchiveRejectsNonTerminalPlan(t *testing.T) {
	f := newArchiveFixture(t, 0)
	specimen := f.registerSpecimen(t, "PPS-2026-104")
	plan := f.createPlan(t, specimen.ID)
	f.approvePlan(t, plan.ID)

	_, err := f.service.ArchivePlan(context.Background(), plan.ID, archiveRequest())
	assertCode(t, err, "plan_not_archivable")
}

func TestArchiveBlockedByUnsettledRelease(t *testing.T) {
	f := newArchiveFixture(t, 0)
	specimen := f.registerSpecimen(t, "PPS-2026-105")
	plan := f.createPlan(t, specimen.ID)
	f.completePlan(t, specimen.ID, plan.ID)
	// Specimen is stabilized but no release record exists yet.

	_, err := f.service.ArchivePlan(context.Background(), plan.ID, archiveRequest())
	assertCode(t, err, "release_unsettled")

	stored, err := f.service.GetSpecimen(context.Background(), specimen.ID)
	if err != nil || stored.Status != domain.SpecimenStabilized {
		t.Fatalf("failed archival must leave specimen stabilized: %v", err)
	}
}

func TestArchiveCancelledPlan(t *testing.T) {
	f := newArchiveFixture(t, 0)
	specimen := f.registerSpecimen(t, "PPS-2026-106")
	plan := f.createPlan(t, specimen.ID)
	if _, err := f.service.CancelPlan(context.Background(), plan.ID, contracts.CancelPlanRequest{CancelledBy: "curator-lin"}); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	result, err := f.service.ArchivePlan(context.Background(), plan.ID, archiveRequest())
	if err != nil {
		t.Fatalf("archive cancelled plan: %v", err)
	}
	if !result.Created || result.Archive.ReleaseID != "" {
		t.Fatalf("cancelled plan archive must not require a release reference: %+v", result)
	}
	if result.Archive.Plan.CancelledBy != "curator-lin" {
		t.Fatal("archive must preserve who cancelled the draft")
	}

	// The cancelled plan is gone from the online set but the specimen is
	// untouched (still planned), so a new plan can still be drafted.
	plans, err := f.service.ListPlansForSpecimen(context.Background(), specimen.ID)
	if err != nil || len(plans) != 0 {
		t.Fatalf("online plans after archival = %v, err=%v", plans, err)
	}
	stored, err := f.service.GetSpecimen(context.Background(), specimen.ID)
	if err != nil || stored.Status != domain.SpecimenPlanned {
		t.Fatalf("specimen status changed while archiving cancelled plan: %v", err)
	}
}

func TestArchiveRejectsIncompleteAuditReference(t *testing.T) {
	f := newArchiveFixture(t, 0)
	specimen := f.registerSpecimen(t, "PPS-2026-107")
	plan := f.createPlan(t, specimen.ID)
	f.completePlan(t, specimen.ID, plan.ID)
	f.releaseSpecimen(t, specimen.ID)

	_, err := f.service.ArchivePlan(context.Background(), plan.ID, contracts.ArchivePlanRequest{
		ArchivedBy:     "keeper-zhou",
		AuditReference: "ticket-42",
	})
	assertCode(t, err, "incomplete_archive_reference")

	// Rejected submission created no archive.
	if _, err := f.service.GetPlanArchive(context.Background(), plan.ID); err == nil {
		t.Fatal("rejected archival must not leave an archive behind")
	}
}

func TestArchivedPlanRejectsFurtherWrites(t *testing.T) {
	f := newArchiveFixture(t, 0)
	specimen := f.registerSpecimen(t, "PPS-2026-108")
	plan := f.createPlan(t, specimen.ID)
	f.completePlan(t, specimen.ID, plan.ID)
	f.releaseSpecimen(t, specimen.ID)
	if _, err := f.service.ArchivePlan(context.Background(), plan.ID, archiveRequest()); err != nil {
		t.Fatalf("archive: %v", err)
	}

	_, err := f.service.LogProgress(context.Background(), plan.ID, contracts.ProgressRequest{
		Stage: "事后补录", Percent: 100, RecordedBy: "preparator-chen",
	})
	assertCode(t, err, "plan_archived")

	if _, err := f.service.ApprovePlan(context.Background(), plan.ID, contracts.ApprovalRequest{ApprovedBy: "x"}); err == nil {
		t.Fatal("approving an archived plan must fail")
	} else {
		assertCode(t, err, "plan_archived")
	}
}

func TestArchiveUnknownAndReadMissingArchive(t *testing.T) {
	f := newArchiveFixture(t, 0)
	if _, err := f.service.ArchivePlan(context.Background(), "plan-missing", archiveRequest()); err == nil {
		t.Fatal("archiving an unknown plan must fail")
	} else {
		assertCode(t, err, "plan_not_found")
	}
	if _, err := f.service.GetPlanArchive(context.Background(), "plan-missing"); err == nil {
		t.Fatal("reading a missing archive must fail")
	} else {
		assertCode(t, err, "plan_archive_not_found")
	}
	if _, err := f.service.GetPlanArchiveByID(context.Background(), "archive-missing"); err == nil {
		t.Fatal("reading a missing archive id must fail")
	} else {
		assertCode(t, err, "plan_archive_not_found")
	}
}

func TestArchiveReadDetectsTampering(t *testing.T) {
	f := newArchiveFixture(t, 0)
	specimen := f.registerSpecimen(t, "PPS-2026-110")
	plan := f.createPlan(t, specimen.ID)
	f.completePlan(t, specimen.ID, plan.ID)
	f.releaseSpecimen(t, specimen.ID)
	result, err := f.service.ArchivePlan(context.Background(), plan.ID, archiveRequest())
	if err != nil {
		t.Fatalf("archive: %v", err)
	}

	// Corrupt the stored archive directly, bypassing the immutable workflow.
	tampered := result.Archive
	tampered.AuditReference = "AUDIT-2026-0001"
	if err := f.repo.Mutate(func(m *repository.Mutation) error {
		m.PutArchive(tampered)
		return nil
	}); err != nil {
		t.Fatalf("inject tampered archive: %v", err)
	}

	if _, err := f.service.GetPlanArchive(context.Background(), plan.ID); err == nil {
		t.Fatal("tampered archive read must fail")
	} else {
		assertCode(t, err, "archive_tampered")
	}
	if _, err := f.service.GetPlanArchiveByID(context.Background(), result.Archive.ID); err == nil {
		t.Fatal("tampered archive lookup by id must fail")
	} else {
		assertCode(t, err, "archive_tampered")
	}
}

func TestCancelOnlyDraftsAndMissing(t *testing.T) {
	f := newArchiveFixture(t, 0)
	specimen := f.registerSpecimen(t, "PPS-2026-109")
	plan := f.createPlan(t, specimen.ID)
	f.approvePlan(t, plan.ID)

	if _, err := f.service.CancelPlan(context.Background(), plan.ID, contracts.CancelPlanRequest{CancelledBy: "curator-lin"}); err == nil {
		t.Fatal("approved plan must not be cancellable")
	} else {
		assertCode(t, err, "plan_not_cancellable")
	}
	if _, err := f.service.CancelPlan(context.Background(), "plan-missing", contracts.CancelPlanRequest{CancelledBy: "curator-lin"}); err == nil {
		t.Fatal("missing plan cancel must not succeed")
	} else {
		assertCode(t, err, "plan_not_found")
	}
}
