package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/model"
)

type withdrawInput struct {
	Reason string `json:"reason"`
}

// resolveVesselPlan loads a plan and confirms the vessel belongs to it.
func resolveVesselPlan(runtime *Runtime, r *http.Request) (model.ConvoyPlan, uuid.UUID, error) {
	planID, err := uuid.Parse(chi.URLParam(r, "planID"))
	if err != nil {
		return model.ConvoyPlan{}, uuid.Nil, err
	}
	vesselID, err := uuid.Parse(chi.URLParam(r, "vesselID"))
	if err != nil {
		return model.ConvoyPlan{}, uuid.Nil, err
	}
	plan, ok := runtime.Planner.Get(planID)
	if !ok {
		return model.ConvoyPlan{}, uuid.Nil, errors.New("plan not found")
	}
	for _, vessel := range plan.Vessels {
		if vessel.ID == vesselID {
			return plan, vesselID, nil
		}
	}
	return model.ConvoyPlan{}, uuid.Nil, errors.New("vessel does not belong to this plan")
}

func allowVesselHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plan, vesselID, err := resolveVesselPlan(runtime, r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		cycleValue, ok := runtime.Cycles.Current(plan.ChamberID)
		if !ok {
			writeError(w, http.StatusConflict, errors.New("active transit not found"))
			return
		}
		decision, err := runtime.Admission.Allow(r.Context(), cycleValue, vesselID)
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		if !decision.Allowed {
			writeError(w, http.StatusConflict, errors.New(decision.Reason))
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{
			"vessel_id": vesselID, "state": model.AdmissionAllowed, "entry": decision.Entry,
		})
	}
}

func vesselEnteredHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plan, vesselID, err := resolveVesselPlan(runtime, r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		admission, err := runtime.Admission.ConfirmEntry(r.Context(), plan.ChamberID, vesselID)
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		ready, reason := runtime.Admission.CanSeal(plan.ChamberID, plan.ID)
		writeJSON(w, http.StatusOK, map[string]any{
			"admission": admission, "ready_to_seal": ready, "seal_reason": reason,
		})
	}
}

func withdrawVesselHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plan, vesselID, err := resolveVesselPlan(runtime, r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		var input withdrawInput
		if r.Body != nil && r.ContentLength != 0 {
			if err := decodeJSON(r, &input); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
		}
		admission, err := runtime.Admission.Withdraw(r.Context(), plan.ChamberID, vesselID, input.Reason)
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"admission": admission})
	}
}

func retryVesselHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plan, vesselID, err := resolveVesselPlan(runtime, r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		admission, err := runtime.Admission.Retry(r.Context(), plan.ChamberID, vesselID)
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"admission": admission})
	}
}
