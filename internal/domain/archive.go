package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"example.com/paleo-prep-station/internal/contracts"
)

// ArchiveDigestFormat identifies the canonical serialization that backs an
// archive digest. It is embedded in every digest input so a future format
// change can never be mistaken for an existing immutable archive.
const ArchiveDigestFormat = "paleo-prep-station/plan-archive/v1"

// PlanArchive is the immutable, verifiable existence fact left behind when a
// finished plan leaves the online collection. The sealed plan and progress
// snapshots never change after creation; Digest binds their exact bytes.
type PlanArchive struct {
	ID              string          `json:"id"`
	PlanID          string          `json:"plan_id"`
	SpecimenID      string          `json:"specimen_id"`
	Plan            PrepPlan        `json:"plan"`
	Progress        []ProgressEntry `json:"progress"`
	ProgressCount   int             `json:"progress_count"`
	ReleaseID       string          `json:"release_id,omitempty"`
	ArchivedBy      string          `json:"archived_by"`
	ArchivedAt      time.Time       `json:"archived_at"`
	ClosedAt        time.Time       `json:"closed_at"`
	RetentionWindow string          `json:"retention_window"`
	DigestAlgorithm string          `json:"digest_algorithm"`
	Digest          string          `json:"digest"`
}

// NewPlanArchive seals a terminal plan. All archive preconditions are enforced
// here inside the caller's transaction:
//
//   - only completed or cancelled plans can be archived;
//   - the plan must have left the recent-query window (retention elapsed);
//   - completed plans must carry a complete progress trail and a finished
//     release (specimen available with its permanent release record);
//   - cancelled plans must not be referenced by any progress entry.
//
// Neither the plan nor the specimen is modified: sealing only produces the
// immutable archive value from their current snapshots.
func NewPlanArchive(
	id string,
	plan PrepPlan,
	progress []ProgressEntry,
	specimen Specimen,
	release *ReleaseRecord,
	archivedBy string,
	retention time.Duration,
	now time.Time,
) (PlanArchive, error) {
	if id == "" {
		return PlanArchive{}, contracts.Internal("missing_generated_id", "归档标识未生成", nil)
	}
	if !plan.IsTerminal() {
		return PlanArchive{}, contracts.Conflict("plan_not_archivable", "只有已完成或已取消方案可以归档")
	}
	closedAt, ok := plan.ClosedAt()
	if !ok {
		return PlanArchive{}, contracts.Conflict("plan_not_archivable", "方案缺少结束时间，不能归档")
	}
	if retention < 0 {
		retention = 0
	}
	stamp := now.UTC()
	if stamp.Sub(closedAt.UTC()) < retention {
		return PlanArchive{}, contracts.Conflict(
			"plan_still_in_recent_scope",
			"方案仍在最近查询保留范围内，暂不能归档",
		)
	}
	archiver, err := requiredText("归档人", archivedBy, 80)
	if err != nil {
		return PlanArchive{}, err
	}
	if specimen.ID != plan.SpecimenID {
		return PlanArchive{}, contracts.Conflict("plan_reference_incomplete", "方案引用的标本与当前标本不一致")
	}

	entries := append([]ProgressEntry(nil), progress...)
	releaseID := ""
	switch plan.Status {
	case PlanCancelled:
		if len(entries) > 0 {
			return PlanArchive{}, contracts.Conflict("plan_reference_incomplete", "已取消方案仍被进度记录引用，不能归档")
		}
	case PlanCompleted:
		if err := validateCompletedTrail(plan, entries); err != nil {
			return PlanArchive{}, err
		}
		if !specimen.IsAvailable() {
			return PlanArchive{}, contracts.Conflict("release_unfinished", "标本尚未完成研究放行，方案不能归档")
		}
		if release == nil {
			return PlanArchive{}, contracts.Conflict("release_unfinished", "缺少放行记录，方案不能归档")
		}
		if release.SpecimenID != specimen.ID {
			return PlanArchive{}, contracts.Conflict("plan_reference_incomplete", "放行记录没有引用归档方案所属标本")
		}
		releaseID = release.ID
	}

	digest := ComputePlanArchiveDigest(plan, entries)
	return PlanArchive{
		ID:              id,
		PlanID:          plan.ID,
		SpecimenID:      plan.SpecimenID,
		Plan:            plan,
		Progress:        entries,
		ProgressCount:   len(entries),
		ReleaseID:       releaseID,
		ArchivedBy:      archiver,
		ArchivedAt:      stamp,
		ClosedAt:        closedAt.UTC(),
		RetentionWindow: retention.String(),
		DigestAlgorithm: "SHA-256",
		Digest:          digest,
	}, nil
}

func validateCompletedTrail(plan PrepPlan, entries []ProgressEntry) error {
	if len(entries) == 0 {
		return contracts.Conflict("plan_reference_incomplete", "已完成方案缺少进度记录，引用不完整")
	}
	last := entries[len(entries)-1]
	if last.PlanID != plan.ID {
		return contracts.Conflict("plan_reference_incomplete", "进度记录没有引用该方案")
	}
	if last.Percent != 100 {
		return contracts.Conflict("plan_reference_incomplete", "方案最后一条进度未达到百分之百，引用不完整")
	}
	previousPercent := 0
	for index, entry := range entries {
		if entry.PlanID != plan.ID {
			return contracts.Conflict("plan_reference_incomplete", "进度记录没有引用该方案")
		}
		if entry.Sequence != index+1 {
			return contracts.Conflict("plan_reference_incomplete", "进度序号不连续，引用不完整")
		}
		if entry.Percent < previousPercent {
			return contracts.Conflict("plan_reference_incomplete", "进度比例不满足单调约束，引用不完整")
		}
		previousPercent = entry.Percent
	}
	return nil
}

type archiveDigestEnvelope struct {
	Format   string          `json:"format"`
	Plan     PrepPlan        `json:"plan"`
	Progress []ProgressEntry `json:"progress"`
}

// ComputePlanArchiveDigest returns the hex SHA-256 over the canonical
// serialization of a sealed plan snapshot. It depends only on the plan and
// progress bytes, never on archive metadata, so re-sealing the same snapshot
// yields the same digest while any tampering yields a different one.
func ComputePlanArchiveDigest(plan PrepPlan, progress []ProgressEntry) string {
	entries := append([]ProgressEntry(nil), progress...)
	payload, err := json.Marshal(archiveDigestEnvelope{
		Format:   ArchiveDigestFormat,
		Plan:     plan,
		Progress: entries,
	})
	if err != nil {
		// All field types are JSON-safe value types; a failure here can only
		// be a programmer error, so fail loudly rather than forge a digest.
		panic(err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// Verify recomputes the digest from the sealed snapshots. A mismatch proves the
// archive content was altered after creation.
func (a PlanArchive) Verify() error {
	actual := ComputePlanArchiveDigest(a.Plan, a.Progress)
	if actual != a.Digest {
		return contracts.Internal(
			"archive_digest_mismatch",
			"归档快照摘要校验失败，内容可能已被篡改",
			nil,
		)
	}
	return nil
}
