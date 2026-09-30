package admission

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/gate"
	"github.com/wyw14/cry-96/internal/model"
	"github.com/wyw14/cry-96/internal/signal"
)

type fakeContact struct{ lost map[uuid.UUID]bool }

func (f fakeContact) LostContact(vesselID uuid.UUID, _ time.Time) bool { return f.lost[vesselID] }

func testCycle(planID uuid.UUID, stage model.TransitStage) model.TransitCycle {
	return model.TransitCycle{
		ID: uuid.New(), ChamberID: "c1", PlanID: planID,
		Direction: model.DirectionUpstream, Stage: stage, Generation: 1,
	}
}

func newHarness(t *testing.T, vessels ...model.Vessel) (*Controller, model.ConvoyPlan, model.TransitCycle) {
	t.Helper()
	plan := model.ConvoyPlan{
		ID: uuid.New(), ChamberID: "c1", Direction: model.DirectionUpstream, Vessels: vessels,
	}
	queue := NewQueue()
	if err := queue.Add(plan.ChamberID, plan.ID, vessels, time.Now()); err != nil {
		t.Fatalf("queue add: %v", err)
	}
	gates := gate.NewState(plan.ChamberID)
	// Upstream-bound vessels enter through the downstream gate.
	if err := gates.Observe(plan.ChamberID, model.DirectionDownstream, model.GateOpen, time.Now()); err != nil {
		t.Fatalf("observe gate: %v", err)
	}
	controller := NewController(queue, gates, signal.NewStore(), fakeContact{lost: map[uuid.UUID]bool{}}, time.Now)
	return controller, plan, testCycle(plan.ID, model.StageAdmitting)
}

func threeVessels(t *testing.T) []model.Vessel {
	t.Helper()
	vessels := make([]model.Vessel, 3)
	for index := range vessels {
		vessel, err := model.NewVessel("Vessel "+string(rune('A'+index)), "CS", 50)
		if err != nil {
			t.Fatalf("vessel: %v", err)
		}
		vessels[index] = vessel
	}
	return vessels
}

// Followers must never be releasable while an earlier vessel is unfinished.
func TestAllowRespectsFIFO(t *testing.T) {
	controller, plan, cycle := newHarness(t, threeVessels(t)...)
	ctx := context.Background()

	if decision, err := controller.Allow(ctx, cycle, plan.Vessels[1].ID); err != nil || decision.Allowed {
		t.Fatalf("follower allow = %v, %v; want withheld", decision.Allowed, err)
	}
	decision, err := controller.Allow(ctx, cycle, plan.Vessels[0].ID)
	if err != nil || !decision.Allowed {
		t.Fatalf("head allow = %v, %v; want allowed", decision.Allowed, err)
	}
	// A second clearance of the same head is a no-op, not a duplicate signal.
	if decision, err := controller.Allow(ctx, cycle, plan.Vessels[0].ID); err != nil || decision.Allowed {
		t.Fatalf("repeat head allow = %v, %v; want withheld", decision.Allowed, err)
	}
	if _, err := controller.ConfirmEntry(plan.ChamberID, plan.Vessels[1].ID); err == nil {
		t.Fatal("follower confirmed before head entered")
	}
	if _, err := controller.ConfirmEntry(plan.ChamberID, plan.Vessels[0].ID); err != nil {
		t.Fatalf("head entry: %v", err)
	}
	// After the head enters, the next standby vessel becomes releasable.
	if decision, err := controller.Allow(ctx, cycle, plan.Vessels[1].ID); err != nil || !decision.Allowed {
		t.Fatalf("vessel B allow = %v, %v; want allowed", decision.Allowed, err)
	}
}

// A withdrawn or lost-contact head blocks every following vessel and sealing.
func TestWithdrawnHeadBlocksFollowersAndSeal(t *testing.T) {
	controller, plan, cycle := newHarness(t, threeVessels(t)...)
	ctx := context.Background()

	if _, err := controller.Allow(ctx, cycle, plan.Vessels[0].ID); err != nil {
		t.Fatalf("allow head: %v", err)
	}
	if _, err := controller.ConfirmEntry(plan.ChamberID, plan.Vessels[0].ID); err != nil {
		t.Fatalf("enter head: %v", err)
	}
	if _, err := controller.Allow(ctx, cycle, plan.Vessels[1].ID); err != nil {
		t.Fatalf("allow vessel B: %v", err)
	}
	if _, err := controller.Withdraw(plan.ChamberID, plan.Vessels[1].ID, "engine failure"); err != nil {
		t.Fatalf("withdraw: %v", err)
	}

	// The withdrawn slot stays at the head; vessel C cannot be released.
	if decision, err := controller.Allow(ctx, cycle, plan.Vessels[2].ID); err != nil || decision.Allowed {
		t.Fatalf("follower released past withdrawal: %+v, %v", decision, err)
	}
	if ready, _ := controller.CanSeal(plan.ChamberID, plan.ID); ready {
		t.Fatal("seal ready while a vessel is withdrawn")
	}
	entry, ok := controller.queue.Find(plan.ChamberID, plan.Vessels[1].ID)
	if !ok || entry.State != model.AdmissionWithdrawn || entry.Reason != "engine failure" {
		t.Fatalf("withdrawn entry = %+v ok=%v", entry, ok)
	}
	if _, err := controller.Retry(plan.ChamberID, plan.Vessels[1].ID); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if decision, err := controller.Allow(ctx, cycle, plan.Vessels[1].ID); err != nil || !decision.Allowed {
		t.Fatalf("re-allow after retry = %v, %v", decision.Allowed, err)
	}
}

// The plan can only seal once every vessel has actually entered.
func TestSealRequiresAllEntered(t *testing.T) {
	controller, plan, cycle := newHarness(t, threeVessels(t)...)
	ctx := context.Background()

	if ready, reason := controller.CanSeal(plan.ChamberID, plan.ID); ready {
		t.Fatalf("seal ready before any entry: %s", reason)
	}
	for _, vessel := range plan.Vessels {
		if decision, err := controller.Allow(ctx, cycle, vessel.ID); err != nil || !decision.Allowed {
			t.Fatalf("allow %s: %+v %v", vessel.Name, decision, err)
		}
		if _, err := controller.ConfirmEntry(plan.ChamberID, vessel.ID); err != nil {
			t.Fatalf("enter %s: %v", vessel.Name, err)
		}
	}
	if ready, reason := controller.CanSeal(plan.ChamberID, plan.ID); !ready {
		t.Fatalf("seal not ready after all entered: %s", reason)
	}
}

// A vessel whose lease has gone silent must not be cleared.
func TestAllowWithholdsLostContact(t *testing.T) {
	vessels := threeVessels(t)
	plan := model.ConvoyPlan{
		ID: uuid.New(), ChamberID: "c2", Direction: model.DirectionUpstream, Vessels: vessels,
	}
	queue := NewQueue()
	if err := queue.Add(plan.ChamberID, plan.ID, vessels, time.Now()); err != nil {
		t.Fatalf("queue add: %v", err)
	}
	gates := gate.NewState(plan.ChamberID)
	_ = gates.Observe(plan.ChamberID, model.DirectionDownstream, model.GateOpen, time.Now())
	contact := fakeContact{lost: map[uuid.UUID]bool{vessels[0].ID: true}}
	controller := NewController(queue, gates, signal.NewStore(), contact, time.Now)
	cycle := testCycle(plan.ID, model.StageAdmitting)

	decision, err := controller.Allow(context.Background(), cycle, vessels[0].ID)
	if err != nil || decision.Allowed {
		t.Fatalf("lost-contact allow = %v, %v; want withheld", decision.Allowed, err)
	}
}

// Allow requires the admitting stage and an open entry gate.
func TestAllowRequiresAdmittingStageAndOpenGate(t *testing.T) {
	controller, plan, _ := newHarness(t, threeVessels(t)...)
	planned := testCycle(plan.ID, model.StagePlanned)
	if decision, err := controller.Allow(context.Background(), planned, plan.Vessels[0].ID); err != nil || decision.Allowed {
		t.Fatalf("planned-stage allow = %v, %v; want withheld", decision.Allowed, err)
	}
	// Close the entry gate and re-check during admitting.
	_ = controller.gates.Observe(plan.ChamberID, model.DirectionDownstream, model.GateClosed, time.Now())
	cycle := testCycle(plan.ID, model.StageAdmitting)
	if decision, err := controller.Allow(context.Background(), cycle, plan.Vessels[0].ID); err != nil || decision.Allowed {
		t.Fatalf("closed-gate allow = %v, %v; want withheld", decision.Allowed, err)
	}
}
