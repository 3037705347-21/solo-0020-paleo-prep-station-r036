package httpapi

import (
	"net/http"

	"example.com/paleo-prep-station/internal/contracts"
)

func (api *API) archivePlan(w http.ResponseWriter, r *http.Request) {
	var request contracts.ArchivePlanRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	archive, err := api.service.ArchivePlan(r.Context(), r.PathValue("id"), request)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Location", "/archives/"+archive.ID)
	writeJSON(w, http.StatusCreated, archive)
}

func (api *API) getArchive(w http.ResponseWriter, r *http.Request) {
	archive, err := api.service.GetArchive(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, archive)
}
