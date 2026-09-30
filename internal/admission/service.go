package admission

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/journal"
	"github.com/wyw14/cry-96/internal/model"
)

// Service records queue changes and drives one-vessel-at-a-time release.
type Service struct {
	controller *Controller
	recorder   *journal.Recorder
	now        func() time.Time
}

func NewService(controller *Controller, recorder *journal.Recorder, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{controller: controller, recorder: recorder, now: now}
}

func (s *Service) Enqueue(ctx context.Context, plan model.ConvoyPlan) error {
	if err := s.controller.queue.Add(plan.ChamberID, plan.ID, plan.Vessels, s.now()); err != nil {
		return err
	}
	_, err := s.recorder.Record(ctx, plan.ChamberID, model.EventPlanCreated, plan.ID, 0, map[string]any{
		"plan_id": plan.ID, "queued": len(plan.Vessels), "at": s.now().UTC(),
	})
	return err
}

func toAdmission(entry QueueEntry) model.VesselAdmission {
	return model.VesselAdmission{
		PlanID: entry.PlanID, VesselID: entry.Vessel.ID, Sequence: entry.Sequence,
		State: entry.State, Reason: entry.Reason, ChangedAt: entry.ChangedAt,
	}
}

// Allow clears the queue head and journals the standby -> allowed transition.
func (s *Service) Allow(ctx context.Context, cycle model.TransitCycle, vesselID uuid.UUID) (Decision, error) {
	decision, err := s.controller.Allow(ctx, cycle, vesselID)
	if err != nil || !decision.Allowed {
		return decision, err
	}
	admission := toAdmission(decision.Entry)
	if _, err := s.recorder.Record(ctx, cycle.ChamberID, model.EventVesselAdmission, cycle.ID, cycle.Generation, admission); err != nil {
		s.controller.queue.RevertAllow(cycle.ChamberID, vesselID, s.now())
		s.controller.signals.Set(vesselID, cycle.ID, cycle.Generation, model.SignalStop, s.now())
		return Decision{}, err
	}
	return decision, nil
}

// ConfirmEntry records allowed -> entered when the vessel is inside.
func (s *Service) ConfirmEntry(ctx context.Context, chamberID string, vesselID uuid.UUID) (model.VesselAdmission, error) {
	entry, err := s.controller.ConfirmEntry(chamberID, vesselID)
	if err != nil {
		return model.VesselAdmission{}, err
	}
	admission := toAdmission(entry)
	if _, err := s.recorder.Record(ctx, chamberID, model.EventVesselAdmission, uuid.Nil, 0, admission); err != nil {
		s.controller.queue.RevertEntry(chamberID, vesselID, s.now())
		return model.VesselAdmission{}, err
	}
	return admission, nil
}

// Withdraw records a failure or lost-contact vessel and blocks followers.
func (s *Service) Withdraw(ctx context.Context, chamberID string, vesselID uuid.UUID, reason string) (model.VesselAdmission, error) {
	entry, err := s.controller.Withdraw(chamberID, vesselID, reason)
	if err != nil {
		return model.VesselAdmission{}, err
	}
	admission := toAdmission(entry)
	// A failed journal leaves the vessel withdrawn: blocking followers is the
	// safe failure mode while the operator resolves the discrepancy.
	_, err = s.recorder.Record(ctx, chamberID, model.EventVesselAdmission, uuid.Nil, 0, admission)
	return admission, err
}

// Retry returns a withdrawn head vessel to standby.
func (s *Service) Retry(ctx context.Context, chamberID string, vesselID uuid.UUID) (model.VesselAdmission, error) {
	entry, err := s.controller.Retry(chamberID, vesselID)
	if err != nil {
		return model.VesselAdmission{}, err
	}
	admission := toAdmission(entry)
	_, err = s.recorder.Record(ctx, chamberID, model.EventVesselAdmission, uuid.Nil, 0, admission)
	return admission, err
}

func (s *Service) CanSeal(chamberID string, planID uuid.UUID) (bool, string) {
	return s.controller.CanSeal(chamberID, planID)
}

func (s *Service) CanOpen(chamberID string, planID uuid.UUID) (model.Vessel, bool, string) {
	return s.controller.CanOpen(chamberID, planID)
}

func (s *Service) RemovePlan(chamberID string, plan model.ConvoyPlan) []QueueEntry {
	return s.controller.queue.RemovePlan(chamberID, plan.ID)
}

func (s *Service) Restore(chamberID string, entries []QueueEntry) {
	s.controller.queue.Restore(chamberID, entries)
}

func (s *Service) List(chamberID string) []QueueEntry {
	return s.controller.queue.List(chamberID)
}

func (s *Service) PlanProgress(chamberID string, planID uuid.UUID) Progress {
	return s.controller.queue.PlanProgress(chamberID, planID)
}
