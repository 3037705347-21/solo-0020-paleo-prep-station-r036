package domain

import (
	"testing"
	"time"

	"example.com/paleo-prep-station/internal/contracts"
)

func archiveTestClock() time.Time {
	return time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
}

func newArchivableCompletedPlan(t *testing.T) (PrepPlan, []ProgressEntry, string) {
	t.Helper()
	now := archiveTestClock()
	specimen, err := NewSpecimen("specimen-1", "PPS-2026-001", "北坡-07", "", "粉砂岩", "", now)
	if err != nil {
		t.Fatalf("specimen: %v", err)
	}
	plan, err := NewPrepPlan("plan-1", specimen, "暴露主要关节面", "手工显微剥离", "可逆树脂", 80, now)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if err := plan.Approve("curator-lin", now.Add(time.Hour)); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if err := plan.Begin(now.Add(2 * time.Hour)); err != nil {
		t.Fatalf("begin: %v", err)
	}
	first, err := NewProgressEntry("progress-1", plan, nil, "矩阵剥离", 60, "", "preparator-chen", now.Add(3*time.Hour))
	if err != nil {
		t.Fatalf("progress 1: %v", err)
	}
	// Mirror the production order: the final entry is built while the plan is
	// still active, and the plan is completed in the same commit afterwards.
	second, err := NewProgressEntry("progress-2", plan, []ProgressEntry{first}, "表面加固", 100, "", "preparator-chen", now.Add(4*time.Hour))
	if err != nil {
		t.Fatalf("progress 2: %v", err)
	}
	if err := plan.Complete(now.Add(4 * time.Hour)); err != nil {
		t.Fatalf("complete: %v", err)
	}
	return plan, []ProgressEntry{first, second}, "release-1"
}

func newArchivableCancelledPlan(t *testing.T) PrepPlan {
	t.Helper()
	now := archiveTestClock()
	specimen, err := NewSpecimen("specimen-2", "PPS-2026-002", "北坡-08", "", "泥岩", "", now)
	if err != nil {
		t.Fatalf("specimen: %v", err)
	}
	plan, err := NewPrepPlan("plan-2", specimen, "暴露主要关节面", "手工显微剥离", "", 80, now)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if err := plan.Cancel("curator-lin", now.Add(time.Hour)); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	return plan
}

func TestNewPlanArchiveCompleted(t *testing.T) {
	plan, progress, releaseID := newArchivableCompletedPlan(t)
	archive, err := NewPlanArchive("archive-1", plan, progress, releaseID, "keeper-zhou", "AUDIT-2026-0014", archiveTestClock().Add(24*time.Hour))
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if archive.PlanID != "plan-1" || archive.SpecimenID != "specimen-1" {
		t.Fatalf("unexpected archive identity: %+v", archive)
	}
	if archive.ReleaseID != "release-1" {
		t.Fatalf("release reference not preserved: %q", archive.ReleaseID)
	}
	if archive.AuditReference != "AUDIT-2026-0014" {
		t.Fatalf("audit reference not normalized: %q", archive.AuditReference)
	}
	if len(archive.Progress) != 2 || archive.ContentDigest == "" {
		t.Fatalf("archive content incomplete: %+v", archive)
	}
	if !archive.MatchesDigest() {
		t.Fatal("fresh archive must match its content digest")
	}
}

func TestNewPlanArchiveCancelledDoesNotRequireRelease(t *testing.T) {
	plan := newArchivableCancelledPlan(t)
	archive, err := NewPlanArchive("archive-2", plan, nil, "", "keeper-zhou", "AUD-2026-X9", archiveTestClock().Add(24*time.Hour))
	if err != nil {
		t.Fatalf("archive cancelled plan: %v", err)
	}
	if archive.ReleaseID != "" {
		t.Fatalf("cancelled plan must not carry a release reference, got %q", archive.ReleaseID)
	}
	if len(archive.Progress) != 0 {
		t.Fatalf("cancelled plan archive must keep a non-nil empty progress slice, got %v", archive.Progress)
	}
	if !archive.MatchesDigest() {
		t.Fatal("fresh archive must match its content digest")
	}
}

func TestNewPlanArchiveRejectsIncompleteReferences(t *testing.T) {
	plan, progress, releaseID := newArchivableCompletedPlan(t)

	cases := []struct {
		name       string
		mutate     func(*PrepPlan, *[]ProgressEntry, *string)
		wantCode   string
		wantKind   contracts.ErrorKind
		archivedBy string
		audit      string
	}{
		{
			name: "non-terminal plan",
			mutate: func(p *PrepPlan, _ *[]ProgressEntry, _ *string) {
				p.Status = PlanActive
			},
			wantCode:   "plan_not_archivable",
			wantKind:   contracts.KindConflict,
			archivedBy: "keeper-zhou",
			audit:      "AUDIT-2026-0014",
		},
		{
			name: "completed without progress",
			mutate: func(_ *PrepPlan, entries *[]ProgressEntry, _ *string) {
				*entries = nil
			},
			wantCode:   "incomplete_archive_reference",
			wantKind:   contracts.KindConflict,
			archivedBy: "keeper-zhou",
			audit:      "AUDIT-2026-0014",
		},
		{
			name: "completed before final percent",
			mutate: func(_ *PrepPlan, entries *[]ProgressEntry, _ *string) {
				*entries = (*entries)[:1]
			},
			wantCode:   "incomplete_archive_reference",
			wantKind:   contracts.KindConflict,
			archivedBy: "keeper-zhou",
			audit:      "AUDIT-2026-0014",
		},
		{
			name: "completed without settled release",
			mutate: func(_ *PrepPlan, _ *[]ProgressEntry, release *string) {
				*release = ""
			},
			wantCode:   "release_unsettled",
			wantKind:   contracts.KindConflict,
			archivedBy: "keeper-zhou",
			audit:      "AUDIT-2026-0014",
		},
		{
			name: "progress belonging to another plan",
			mutate: func(_ *PrepPlan, entries *[]ProgressEntry, _ *string) {
				(*entries)[0].PlanID = "plan-other"
			},
			wantCode:   "incomplete_archive_reference",
			wantKind:   contracts.KindConflict,
			archivedBy: "keeper-zhou",
			audit:      "AUDIT-2026-0014",
		},
		{
			name:       "missing audit reference",
			mutate:     func(*PrepPlan, *[]ProgressEntry, *string) {},
			wantCode:   "incomplete_archive_reference",
			wantKind:   contracts.KindInvalid,
			archivedBy: "keeper-zhou",
			audit:      "   ",
		},
		{
			name:       "malformed audit reference",
			mutate:     func(*PrepPlan, *[]ProgressEntry, *string) {},
			wantCode:   "incomplete_archive_reference",
			wantKind:   contracts.KindInvalid,
			archivedBy: "keeper-zhou",
			audit:      "ticket-42",
		},
		{
			name:       "missing archivist",
			mutate:     func(*PrepPlan, *[]ProgressEntry, *string) {},
			wantCode:   "required_field",
			wantKind:   contracts.KindInvalid,
			archivedBy: "",
			audit:      "AUDIT-2026-0014",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := plan
			entries := append([]ProgressEntry(nil), progress...)
			release := releaseID
			tc.mutate(&p, &entries, &release)
			_, err := NewPlanArchive("archive-x", p, entries, release, tc.archivedBy, tc.audit, archiveTestClock())
			appErr, ok := contracts.AsError(err)
			if !ok {
				t.Fatalf("expected contracts error, got %v", err)
			}
			if appErr.Code != tc.wantCode || appErr.Kind != tc.wantKind {
				t.Fatalf("expected %s/%s, got %s/%s", tc.wantKind, tc.wantCode, appErr.Kind, appErr.Code)
			}
		})
	}
}

func TestPlanArchiveDigestDetectsTampering(t *testing.T) {
	plan, progress, releaseID := newArchivableCompletedPlan(t)
	first, err := NewPlanArchive("archive-1", plan, progress, releaseID, "keeper-zhou", "AUDIT-2026-0014", archiveTestClock())
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	second, err := NewPlanArchive("archive-dup", plan, progress, releaseID, "keeper-zhou", " audit-2026-0014 ", archiveTestClock())
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if first.ContentDigest != second.ContentDigest {
		t.Fatal("identical preserved content must produce identical digests")
	}

	tampered := first
	tampered.ArchivedBy = "intruder"
	if tampered.MatchesDigest() {
		t.Fatal("mutating an immutable archive must break its digest")
	}
	tampered = first
	tampered.Plan.Status = PlanDraft
	if tampered.MatchesDigest() {
		t.Fatal("plan state tampering must break the digest")
	}
	tampered = first
	tampered.Progress[0].Percent = 5
	if tampered.MatchesDigest() {
		t.Fatal("progress tampering must break the digest")
	}
}

func TestCancelRecordsOperator(t *testing.T) {
	plan := newArchivableCancelledPlan(t)
	if plan.CancelledBy != "curator-lin" {
		t.Fatalf("cancelled by not recorded: %q", plan.CancelledBy)
	}
}
