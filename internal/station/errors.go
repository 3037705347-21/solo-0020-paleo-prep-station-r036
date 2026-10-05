package station

import "example.com/paleo-prep-station/internal/contracts"

func notFoundSpecimen(id string) error {
	return contracts.NotFound("specimen_not_found", "未找到标本 "+id)
}

func notFoundPlan(id string) error {
	return contracts.NotFound("plan_not_found", "未找到修复方案 "+id)
}

func notFoundArchive(planID string) error {
	return contracts.NotFound("plan_archive_not_found", "未找到方案 "+planID+" 的归档记录")
}

// archivedPlanError keeps the online 404 distinct from a genuine missing plan
// and points the caller at the surviving archive record.
func archivedPlanError(planID, archiveID string) error {
	return contracts.NotFoundReference(
		"plan_archived",
		"修复方案 "+planID+" 已归档，不在在线方案集合中",
		"/plan-archives/"+archiveID,
	)
}
