package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"example.com/paleo-prep-station/internal/contracts"
)

// auditReferencePattern accepts uppercase business codes such as AUDIT-2026-014.
var auditReferencePattern = regexp.MustCompile(`^[A-Z][A-Z0-9]{2,7}-[0-9]{4}-[0-9A-Z]{1,12}$`)

// PlanArchive is the immutable record left behind when a terminal plan leaves
// the online set. It preserves the final plan snapshot, every progress entry,
// the settled release reference (for completed plans) and the audit ticket
// that proves the removal was authorized. Once created it must never change;
// ContentDigest binds all preserved content into a locatable fact.
type PlanArchive struct {
	ID             string          `json:"id"`
	PlanID         string          `json:"plan_id"`
	SpecimenID     string          `json:"specimen_id"`
	Plan           PrepPlan        `json:"plan"`
	Progress       []ProgressEntry `json:"progress"`
	ReleaseID      string          `json:"release_id,omitempty"`
	AuditReference string          `json:"audit_reference"`
	ArchivedBy     string          `json:"archived_by"`
	ContentDigest  string          `json:"content_digest"`
	CreatedAt      time.Time       `json:"created_at"`
}

// NewPlanArchive builds the archive and validates both the authorization
// inputs and the structural completeness of the preserved references.
func NewPlanArchive(
	id string,
	plan PrepPlan,
	progress []ProgressEntry,
	releaseID string,
	archivedBy string,
	auditReference string,
	now time.Time,
) (PlanArchive, error) {
	if id == "" {
		return PlanArchive{}, contracts.Internal("missing_generated_id", "归档标识未生成", nil)
	}
	switch {
	case plan.Status == PlanCompleted:
		if len(progress) == 0 {
			return PlanArchive{}, contracts.Conflict("incomplete_archive_reference", "已完成方案缺少进度记录，引用不完整")
		}
		if progress[len(progress)-1].Percent != 100 {
			return PlanArchive{}, contracts.Conflict("incomplete_archive_reference", "已完成方案最后一条进度未达到百分之百")
		}
		if strings.TrimSpace(releaseID) == "" {
			return PlanArchive{}, contracts.Conflict("release_unsettled", "已完成方案的研究放行尚未办结，不能归档")
		}
	case plan.Status == PlanCancelled:
		if strings.TrimSpace(plan.CancelledBy) == "" {
			return PlanArchive{}, contracts.Conflict("incomplete_archive_reference", "已取消方案缺少取消记录，引用不完整")
		}
	default:
		return PlanArchive{}, contracts.Conflict("plan_not_archivable", "只有已完成或已取消方案可以归档")
	}
	for _, entry := range progress {
		if entry.PlanID != plan.ID {
			return PlanArchive{}, contracts.Conflict("incomplete_archive_reference", "进度记录与方案不一致，引用不完整")
		}
	}
	archiver, err := requiredText("归档人", archivedBy, 80)
	if err != nil {
		return PlanArchive{}, err
	}
	audit, err := validateAuditReference(auditReference)
	if err != nil {
		return PlanArchive{}, err
	}
	entries := progress
	if entries == nil {
		entries = []ProgressEntry{}
	}
	stamp := now.UTC()
	archive := PlanArchive{
		ID:             id,
		PlanID:         plan.ID,
		SpecimenID:     plan.SpecimenID,
		Plan:           plan,
		Progress:       entries,
		ReleaseID:      strings.TrimSpace(releaseID),
		AuditReference: audit,
		ArchivedBy:     archiver,
		CreatedAt:      stamp,
	}
	digest, err := digestArchiveContent(plan, entries, archive.ReleaseID, audit, archiver, stamp)
	if err != nil {
		return PlanArchive{}, contracts.Internal("archive_digest_failed", "归档摘要计算失败", err)
	}
	archive.ContentDigest = digest
	return archive, nil
}

// MatchesDigest re-derives the digest from the preserved content. Any mutation
// of the immutable archive breaks the equality.
func (a PlanArchive) MatchesDigest() bool {
	digest, err := digestArchiveContent(a.Plan, a.Progress, a.ReleaseID, a.AuditReference, a.ArchivedBy, a.CreatedAt)
	if err != nil {
		return false
	}
	return digest == a.ContentDigest
}

func validateAuditReference(value string) (string, error) {
	cleaned := strings.ToUpper(strings.TrimSpace(value))
	if cleaned == "" {
		return "", contracts.Invalid("incomplete_archive_reference", "归档必须提供完整的审计引用编号")
	}
	if len([]rune(cleaned)) > 40 {
		return "", contracts.Invalid("incomplete_archive_reference", "审计引用编号长度超出限制")
	}
	if !auditReferencePattern.MatchString(cleaned) {
		return "", contracts.Invalid("incomplete_archive_reference", "审计引用编号格式必须为 AUDIT-2026-0001 形式")
	}
	return cleaned, nil
}

type archiveDigestPayload struct {
	Plan           PrepPlan        `json:"plan"`
	Progress       []ProgressEntry `json:"progress"`
	ReleaseID      string          `json:"release_id"`
	AuditReference string          `json:"audit_reference"`
	ArchivedBy     string          `json:"archived_by"`
	CreatedAt      time.Time       `json:"created_at"`
}

func digestArchiveContent(
	plan PrepPlan,
	progress []ProgressEntry,
	releaseID string,
	auditReference string,
	archivedBy string,
	createdAt time.Time,
) (string, error) {
	payload, err := json.Marshal(archiveDigestPayload{
		Plan:           plan,
		Progress:       progress,
		ReleaseID:      releaseID,
		AuditReference: auditReference,
		ArchivedBy:     archivedBy,
		CreatedAt:      createdAt.UTC(),
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}
