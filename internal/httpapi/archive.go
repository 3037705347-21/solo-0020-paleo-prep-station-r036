package httpapi

import (
	"net/http"

	"example.com/paleo-prep-station/internal/contracts"
)

func (api *API) cancelPlan(w http.ResponseWriter, r *http.Request) {
	var request contracts.CancelPlanRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	value, err := api.service.CancelPlan(r.Context(), r.PathValue("id"), request)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (api *API) archivePlan(w http.ResponseWriter, r *http.Request) {
	var request contracts.ArchivePlanRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	result, err := api.service.ArchivePlan(r.Context(), r.PathValue("id"), request)
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
		w.Header().Set("Location", "/plan-archives/"+result.Archive.ID)
	}
	writeJSON(w, status, result.Archive)
}

// getPlanArchive answers the locatable read attached to a plan id: GET
// /plans/{id}/archive. This is how callers prove an archived plan still
// exists after it disappeared from the online plan set.
func (api *API) getPlanArchive(w http.ResponseWriter, r *http.Request) {
	value, err := api.service.GetPlanArchive(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (api *API) getArchiveByID(w http.ResponseWriter, r *http.Request) {
	value, err := api.service.GetPlanArchiveByID(r.Context(), r.PathValue("archive_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (api *API) listArchivesForSpecimen(w http.ResponseWriter, r *http.Request) {
	values, err := api.service.ListArchivesForSpecimen(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": values})
}
