package cycle

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/gate"
	"github.com/wyw14/cry-96/internal/hydraulics"
	"github.com/wyw14/cry-96/internal/journal"
	"github.com/wyw14/cry-96/internal/model"
)

type fakeAdmissionGate struct {
	sealReady  bool
	sealReason string
	openReady  bool
}

func (f *fakeAdmissionGate) CanSeal(string, uuid.UUID) (bool, string) {
	return f.sealReady, f.sealReason
}

func (f *fakeAdmissionGate) CanOpen(string, uuid.UUID) (model.Vessel, bool, string) {
	return model.Vessel{}, f.openReady, ""
}

func newCycleHarness(t *testing.T) (*Coordinator, *gate.Controller, model.ConvoyPlan, *fakeAdmissionGate) {
	t.Helper()
	now := func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	recorder := journal.NewRecorder(journal.NewMemoryStore(), now)
	gates := gate.NewController(gate.NewState("c1"), gate.NewCommands(), recorder, now)
	ramp := hydraulics.NewRamp(time.Millisecond, now)
	hydraulicsController := hydraulics.NewController(gates.Commands(), recorder, ramp, now)
	coordinator := NewCoordinator(gates, hydraulicsController, recorder, now)
	admissionGate := &fakeAdmissionGate{openReady: true, sealReady: false, sealReason: "vessel still entering"}
	coordinator.BindAdmission(admissionGate, admissionGate)

	vessel, err := model.NewVessel("River Star", "RS-1", 80)
	if err != nil {
		t.Fatalf("vessel: %v", err)
	}
	plan, err := model.NewConvoyPlan("c1", model.DirectionUpstream, []model.Vessel{vessel}, now())
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return coordinator, gates, plan, admissionGate
}

func settle(t *testing.T, ctx context.Context, coordinator *Coordinator, gates *gate.Controller, cycle model.TransitCycle, direction model.Direction, open bool) error {
	t.Helper()
	command, err := gates.Request(ctx, cycle, direction, open)
	if err != nil {
		t.Fatalf("issue command: %v", err)
	}
	ack := model.DeviceAck{
		CommandID: command.ID, CycleID: command.CycleID, ChamberID: command.ChamberID,
		Generation: command.Generation, Epoch: command.Epoch, Device: command.Device,
		Settled: true, ObservedAt: time.Now(),
	}
	return coordinator.ApplyGateAck(ctx, ack)
}

// The Admitting -> Sealed transition must wait for every vessel to enter.
func TestSealBlockedUntilAdmissionComplete(t *testing.T) {
	ctx := context.Background()
	coordinator, gates, plan, admissionGate := newCycleHarness(t)

	cycle, err := coordinator.Start(ctx, plan)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := settle(t, ctx, coordinator, gates, cycle, model.DirectionDownstream, true); err != nil {
		t.Fatalf("open entry gate: %v", err)
	}
	cycle, _ = coordinator.Current("c1")
	if cycle.Stage != model.StageAdmitting {
		t.Fatalf("stage after open = %s; want admitting", cycle.Stage)
	}

	// Closing (sealing) while admission is incomplete must be rejected and the
	// cycle must remain in the admitting stage.
	if err := settle(t, ctx, coordinator, gates, cycle, model.DirectionDownstream, false); err == nil {
		t.Fatal("sealed before every vessel entered")
	}
	cycle, _ = coordinator.Current("c1")
	if cycle.Stage != model.StageAdmitting {
		t.Fatalf("stage after rejected seal = %s; want admitting", cycle.Stage)
	}

	// Once every vessel has entered, the close ack advances to sealed.
	admissionGate.sealReady = true
	admissionGate.sealReason = ""
	if err := settle(t, ctx, coordinator, gates, cycle, model.DirectionDownstream, false); err != nil {
		t.Fatalf("seal after admission complete: %v", err)
	}
	cycle, _ = coordinator.Current("c1")
	if cycle.Stage != model.StageSealed {
		t.Fatalf("stage after seal = %s; want sealed", cycle.Stage)
	}
}

// Opening the entry gate is refused until a head vessel is standing by.
func TestOpenBlockedWithoutStandbyHead(t *testing.T) {
	ctx := context.Background()
	coordinator, gates, plan, admissionGate := newCycleHarness(t)
	cycle, err := coordinator.Start(ctx, plan)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	admissionGate.openReady = false
	if err := settle(t, ctx, coordinator, gates, cycle, model.DirectionDownstream, true); err == nil {
		t.Fatal("entry gate opened with no standing-by head vessel")
	}
	cycle, _ = coordinator.Current("c1")
	if cycle.Stage != model.StagePlanned {
		t.Fatalf("stage after rejected open = %s; want planned", cycle.Stage)
	}
}
