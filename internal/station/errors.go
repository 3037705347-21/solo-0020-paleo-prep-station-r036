package station

import "example.com/paleo-prep-station/internal/contracts"

func notFoundSpecimen(id string) error {
	return contracts.NotFound("specimen_not_found", "未找到标本 "+id)
}

func notFoundPlan(id string) error {
	return contracts.NotFound("plan_not_found", "未找到修复方案 "+id)
}

func notFoundArchive(id string) error {
	return contracts.NotFound("archive_not_found", "未找到方案归档 "+id)
}

func withArchiveLocator(err error, archiveID string) error {
	if appErr, ok := err.(*contracts.Error); ok {
		return appErr.WithDetail("archive_id", archiveID)
	}
	return err
}

// archivedPlanRead signals an online read whose plan left the collection. It
// stays a not-found instead of silently serving an empty snapshot, and carries
// the archive id so the immutable existence fact remains locatable.
func archivedPlanRead(planID, archiveID string) error {
	return withArchiveLocator(
		contracts.NotFound("plan_archived", "修复方案 "+planID+" 已归档，不在在线查询范围内"),
		archiveID,
	)
}

// archivedPlanWrite rejects submissions against a sealed plan as a conflict.
// Sealing never changes plan state, so the caller must see an explicit refusal
// rather than a vanished object.
func archivedPlanWrite(planID, archiveID string) error {
	return withArchiveLocator(
		contracts.Conflict("plan_archived", "修复方案 "+planID+" 已归档，不能再提交变更"),
		archiveID,
	)
}

func alreadyArchived(archiveID string) error {
	return withArchiveLocator(
		contracts.Conflict("plan_already_archived", "修复方案已归档，重复归档不会生成第二份结果"),
		archiveID,
	)
}
