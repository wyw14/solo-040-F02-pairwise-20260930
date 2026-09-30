package admission

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/gate"
	"github.com/wyw14/cry-96/internal/model"
	"github.com/wyw14/cry-96/internal/signal"
)

type Decision struct {
	Allowed  bool       `json:"allowed"`
	Reason   string     `json:"reason"`
	VesselID uuid.UUID  `json:"vessel_id,omitempty"`
	Entry    QueueEntry `json:"entry,omitempty"`
}

// ContactChecker reports whether a lease-tracked vessel has lost contact.
type ContactChecker interface {
	LostContact(vesselID uuid.UUID, now time.Time) bool
}

// Controller grants admission one vessel at a time during the admitting stage.
type Controller struct {
	queue   *Queue
	gates   *gate.State
	signals *signal.Store
	contact ContactChecker
	now     func() time.Time
}

func NewController(queue *Queue, gates *gate.State, signals *signal.Store, contact ContactChecker, now func() time.Time) *Controller {
	if now == nil {
		now = time.Now
	}
	return &Controller{queue: queue, gates: gates, signals: signals, contact: contact, now: now}
}

// entryGate is the gate through which a travelling vessel enters the chamber.
func entryGate(cycle model.TransitCycle) model.Direction {
	return cycle.Direction.Opposite()
}

// Allow clears the FIFO head to enter one vessel at a time. The transit must
// be in the admitting stage with the entry gate open, and the vessel must
// still be in contact. Following vessels stay held.
func (c *Controller) Allow(ctx context.Context, cycle model.TransitCycle, vesselID uuid.UUID) (Decision, error) {
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}
	entry, ok := c.queue.Peek(cycle.ChamberID)
	if !ok {
		return Decision{Reason: "queue empty"}, nil
	}
	if entry.PlanID != cycle.PlanID {
		return Decision{VesselID: entry.Vessel.ID, Reason: "head vessel belongs to another plan"}, nil
	}
	if entry.Vessel.ID != vesselID {
		return Decision{VesselID: entry.Vessel.ID, Reason: "only the queue head vessel may be cleared"}, nil
	}
	if entry.State != model.AdmissionStandby {
		return Decision{VesselID: vesselID, Entry: entry, Reason: "head vessel is not standing by"}, nil
	}
	if cycle.Stage != model.StageAdmitting {
		return Decision{VesselID: vesselID, Reason: "vessels can only be cleared during the admitting stage"}, nil
	}
	if position := c.gates.Position(cycle.ChamberID, entryGate(cycle)).Position; position != model.GateOpen {
		return Decision{VesselID: vesselID, Reason: "entry gate is not open"}, nil
	}
	if c.contact != nil && c.contact.LostContact(vesselID, c.now()) {
		return Decision{VesselID: vesselID, Reason: "vessel has lost contact; admission withheld"}, nil
	}
	allowed, moved := c.queue.Allow(cycle.ChamberID, vesselID, c.now())
	if !moved {
		return Decision{}, errors.New("failed to commit admission clearance")
	}
	c.signals.Set(vesselID, cycle.ID, cycle.Generation, model.SignalProceed, c.now())
	return Decision{Allowed: true, VesselID: vesselID, Entry: allowed, Reason: "vessel cleared to enter"}, nil
}

// ConfirmEntry records that a cleared vessel is actually inside the chamber.
func (c *Controller) ConfirmEntry(chamberID string, vesselID uuid.UUID) (QueueEntry, error) {
	entry, ok := c.queue.ConfirmEntry(chamberID, vesselID, c.now())
	if !ok {
		return QueueEntry{}, errors.New("vessel has not been cleared to enter")
	}
	return entry, nil
}

// Withdraw takes a failed or lost-contact vessel out of the running. Its slot
// stays at the queue head so no following vessel can be released past it, and
// its field signal is forced back to stop.
func (c *Controller) Withdraw(chamberID string, vesselID uuid.UUID, reason string) (QueueEntry, error) {
	if reason == "" {
		reason = "vessel withdrawn from admission"
	}
	entry, ok := c.queue.Withdraw(chamberID, vesselID, reason, c.now())
	if !ok {
		return QueueEntry{}, errors.New("vessel cannot withdraw in its current state")
	}
	if state, exists := c.signals.Get(vesselID); exists {
		c.signals.Set(vesselID, state.CycleID, state.Generation, model.SignalStop, c.now())
	}
	return entry, nil
}

// Retry returns a withdrawn head vessel to standby for a fresh clearance.
func (c *Controller) Retry(chamberID string, vesselID uuid.UUID) (QueueEntry, error) {
	entry, ok := c.queue.Retry(chamberID, vesselID, c.now())
	if !ok {
		return QueueEntry{}, errors.New("only a withdrawn head vessel can retry admission")
	}
	return entry, nil
}

func (c *Controller) OwnerAllowed(vesselID uuid.UUID, owner string, token uint64, lease model.Lease, now time.Time) bool {
	return lease.VesselID == vesselID && lease.Owner == owner &&
		lease.FencingToken == token && lease.Active(now)
}

// CanOpen gates opening the entry gate at the start of admission: the queue
// head must be a standing-by vessel belonging to the active plan.
func (c *Controller) CanOpen(chamberID string, planID uuid.UUID) (model.Vessel, bool, string) {
	entry, ok := c.queue.Peek(chamberID)
	if !ok {
		return model.Vessel{}, false, "queue empty"
	}
	if entry.PlanID != planID {
		return model.Vessel{}, false, "head vessel belongs to another plan"
	}
	if entry.State == model.AdmissionWithdrawn {
		return model.Vessel{}, false, "head vessel has withdrawn; retry or resolve it before opening"
	}
	if entry.State != model.AdmissionStandby {
		return model.Vessel{}, false, "head vessel is not standing by for entry"
	}
	return entry.Vessel, true, ""
}

// CanSeal reports whether every vessel of the plan has actually entered, which
// is the precondition for closing the gate and leaving the admitting stage.
func (c *Controller) CanSeal(chamberID string, planID uuid.UUID) (bool, string) {
	progress := c.queue.PlanProgress(chamberID, planID)
	if progress.Total == 0 {
		return false, "plan has no vessels queued"
	}
	if progress.Withdrawn > 0 {
		return false, "a vessel has withdrawn and must retry before sealing"
	}
	if progress.Entered < progress.Total {
		return false, "not every vessel has entered the chamber"
	}
	return true, ""
}

func (c *Controller) Queue() *Queue { return c.queue }
