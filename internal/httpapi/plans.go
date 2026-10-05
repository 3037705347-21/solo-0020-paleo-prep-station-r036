package httpapi

import (
	"net/http"

	"example.com/paleo-prep-station/internal/contracts"
	"example.com/paleo-prep-station/internal/domain"
	"example.com/paleo-prep-station/internal/station"
)

func (api *API) createPlan(w http.ResponseWriter, r *http.Request) {
	var request contracts.CreatePlanRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	value, err := api.service.CreatePlan(r.Context(), r.PathValue("id"), request)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Location", "/plans/"+value.ID)
	writeJSON(w, http.StatusCreated, value)
}

func (api *API) listPlansForSpecimen(w http.ResponseWriter, r *http.Request) {
	listing, err := api.service.ListPlansForSpecimen(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if listing.Items == nil {
		listing.Items = []domain.PrepPlan{}
	}
	if listing.Archived == nil {
		listing.Archived = []station.ArchivedPlanRef{}
	}
	writeJSON(w, http.StatusOK, listing)
}

func (api *API) getPlan(w http.ResponseWriter, r *http.Request) {
	value, err := api.service.GetPlan(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (api *API) approvePlan(w http.ResponseWriter, r *http.Request) {
	var request contracts.ApprovalRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	value, err := api.service.ApprovePlan(r.Context(), r.PathValue("id"), request)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

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
