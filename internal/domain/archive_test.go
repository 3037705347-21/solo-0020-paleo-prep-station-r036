package domain

import (
	"testing"
	"time"

	"example.com/paleo-prep-station/internal/contracts"
)

func testClock() time.Time {
	return time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
}

func sealedCompletedPlan(t *testing.T, now time.Time) (PrepPlan, []ProgressEntry, Specimen, *ReleaseRecord) {
	t.Helper()
	specimen, err := NewSpecimen("specimen-1", "PPS-T-1", "field", "", "泥岩", "", now)
	if err != nil {
		t.Fatalf("new specimen: %v", err)
	}
	plan, err := NewPrepPlan("plan-1", specimen, "暴露主要关节面", "显微剥离", "", 80, now)
	if err != nil {
		t.Fatalf("new plan: %v", err)
	}
	if err := specimen.MarkPlanned(now); err != nil {
		t.Fatalf("mark planned: %v", err)
	}
	if err := plan.Approve("curator-lin", now); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if err := specimen.MarkPreparing(now); err != nil {
		t.Fatalf("mark preparing: %v", err)
	}
	if err := plan.Begin(now); err != nil {
		t.Fatalf("begin: %v", err)
	}
	first, err := NewProgressEntry("progress-1", plan, nil, "剥离", 60, "", "chen", now)
	if err != nil {
		t.Fatalf("first progress: %v", err)
	}
	entries := []ProgressEntry{first}
	final, err := NewProgressEntry("progress-2", plan, entries, "加固", 100, "", "chen", now)
	if err != nil {
		t.Fatalf("final progress: %v", err)
	}
	entries = append(entries, final)
	if err := plan.Complete(now); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if err := specimen.MarkStabilized(now); err != nil {
		t.Fatalf("stabilize: %v", err)
	}
	release, err := NewReleaseRecord("release-1", specimen, "研究", "研究组", "keeper-zhou", now)
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := specimen.MarkAvailable(now); err != nil {
		t.Fatalf("available: %v", err)
	}
	return plan, entries, specimen, &release
}

func TestNewPlanArchive_CompletedPlanSealsDigestAndFact(t *testing.T) {
	now := testClock()
	plan, entries, specimen, release := sealedCompletedPlan(t, now)
	later := now.Add(31 * 24 * time.Hour)

	archive, err := NewPlanArchive("archive-1", plan, entries, specimen, release, "registrar-gao", 30*24*time.Hour, later)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if archive.PlanID != plan.ID || archive.SpecimenID != specimen.ID {
		t.Fatalf("archive does not bind plan/specimen: %+v", archive)
	}
	if archive.ProgressCount != 2 || archive.ReleaseID != "release-1" {
		t.Fatalf("archive snapshot missing trail/release: %+v", archive)
	}
	if len(archive.Digest) != 64 || archive.DigestAlgorithm != "SHA-256" {
		t.Fatalf("archive must carry a sha-256 digest, got %q", archive.Digest)
	}
	if err := archive.Verify(); err != nil {
		t.Fatalf("fresh archive must verify: %v", err)
	}

	// The digest is a property of the sealed snapshot only: re-sealing the
	// same plan and progress at a different time produces the same digest.
	rebuilt, err := NewPlanArchive("archive-other", plan, entries, specimen, release, "someone-else", 0, later.Add(time.Hour))
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if rebuilt.Digest != archive.Digest {
		t.Fatalf("same snapshot must yield same digest: %q vs %q", rebuilt.Digest, archive.Digest)
	}
}

func TestPlanArchive_DetectsTamperedSnapshot(t *testing.T) {
	now := testClock()
	plan, entries, specimen, release := sealedCompletedPlan(t, now)
	archive, err := NewPlanArchive("archive-1", plan, entries, specimen, release, "registrar-gao", 0, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("archive: %v", err)
	}

	tampered := archive
	tampered.Plan.Objective = "被篡改的修复目标"
	if err := tampered.Verify(); err == nil {
		t.Fatal("mutating the sealed plan must break the digest")
	} else if appErr, ok := contracts.AsError(err); !ok || appErr.Code != "archive_digest_mismatch" {
		t.Fatalf("expected archive_digest_mismatch, got %v", err)
	}

	// Copy the backing slice so the tampered variant does not alias the
	// original archive's progress.
	tamperedProgress := archive
	tamperedProgress.Progress = append([]ProgressEntry(nil), archive.Progress...)
	tamperedProgress.Progress[1].Percent = 90
	if err := tamperedProgress.Verify(); err == nil {
		t.Fatal("mutating sealed progress must break the digest")
	}

	if err := archive.Verify(); err != nil {
		t.Fatalf("original archive must still verify after failed copies: %v", err)
	}
}

func TestNewPlanArchive_RejectsRecentNonTerminalAndIncompleteReferences(t *testing.T) {
	now := testClock()

	t.Run("still in recent scope", func(t *testing.T) {
		plan, entries, specimen, release := sealedCompletedPlan(t, now)
		_, err := NewPlanArchive("archive-1", plan, entries, specimen, release, "registrar-gao", 24*time.Hour, now.Add(time.Hour))
		assertConflictCode(t, err, "plan_still_in_recent_scope")
	})

	t.Run("release unfinished", func(t *testing.T) {
		plan, entries, specimen, _ := sealedCompletedPlan(t, now)
		// Revert the specimen to stabilized without a release.
		specimen.Status = SpecimenStabilized
		specimen.Version--
		_, err := NewPlanArchive("archive-1", plan, entries, specimen, nil, "registrar-gao", 0, now.Add(time.Hour))
		assertConflictCode(t, err, "release_unfinished")
	})

	t.Run("trail missing closing entry", func(t *testing.T) {
		plan, entries, specimen, release := sealedCompletedPlan(t, now)
		// Drop the one-hundred-percent closing record: references incomplete.
		_, err := NewPlanArchive("archive-1", plan, entries[:1], specimen, release, "registrar-gao", 0, now.Add(time.Hour))
		assertConflictCode(t, err, "plan_reference_incomplete")
	})

	t.Run("non-terminal plan", func(t *testing.T) {
		specimen, err := NewSpecimen("specimen-2", "PPS-T-2", "field", "", "泥岩", "", now)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := NewPrepPlan("plan-2", specimen, "暴露主要关节面", "显微剥离", "", 80, now)
		if err != nil {
			t.Fatal(err)
		}
		_, err = NewPlanArchive("archive-2", plan, nil, specimen, nil, "registrar-gao", 0, now.Add(time.Hour))
		assertConflictCode(t, err, "plan_not_archivable")
	})

	t.Run("cancelled plan still referenced", func(t *testing.T) {
		specimen, err := NewSpecimen("specimen-3", "PPS-T-3", "field", "", "泥岩", "", now)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := NewPrepPlan("plan-3", specimen, "暴露主要关节面", "显微剥离", "", 80, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := plan.Cancel("curator-lin", now); err != nil {
			t.Fatal(err)
		}
		dangling := []ProgressEntry{{ID: "progress-x", PlanID: plan.ID, Sequence: 1, Percent: 10}}
		_, err = NewPlanArchive("archive-3", plan, dangling, specimen, nil, "registrar-gao", 0, now.Add(time.Hour))
		assertConflictCode(t, err, "plan_reference_incomplete")
	})

	t.Run("cancelled plan with no references archives", func(t *testing.T) {
		specimen, err := NewSpecimen("specimen-4", "PPS-T-4", "field", "", "泥岩", "", now)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := NewPrepPlan("plan-4", specimen, "暴露主要关节面", "显微剥离", "", 80, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := plan.Cancel("curator-lin", now); err != nil {
			t.Fatal(err)
		}
		archive, err := NewPlanArchive("archive-4", plan, nil, specimen, nil, "registrar-gao", 0, now.Add(time.Hour))
		if err != nil {
			t.Fatalf("cancelled plan should archive: %v", err)
		}
		if archive.ProgressCount != 0 || archive.ReleaseID != "" {
			t.Fatalf("cancelled archive must carry no progress/release: %+v", archive)
		}
	})
}

func assertConflictCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected conflict %s, got nil", code)
	}
	appErr, ok := contracts.AsError(err)
	if !ok {
		t.Fatalf("expected contracts error, got %T: %v", err, err)
	}
	if appErr.Kind != contracts.KindConflict || appErr.Code != code {
		t.Fatalf("expected conflict %s, got %s/%s", code, appErr.Kind, appErr.Code)
	}
}
