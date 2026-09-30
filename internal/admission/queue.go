package admission

import (
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/model"
)

type QueueEntry struct {
	Vessel    model.Vessel         `json:"vessel"`
	PlanID    uuid.UUID            `json:"plan_id"`
	Sequence  int                  `json:"sequence"`
	State     model.AdmissionState `json:"state"`
	Reason    string               `json:"reason,omitempty"`
	ChangedAt time.Time            `json:"changed_at"`
}

// Released reports whether the vessel has already been shown a proceed signal.
func (e QueueEntry) Released() bool {
	return e.State == model.AdmissionAllowed || e.State == model.AdmissionEntered
}

// Progress summarises one plan inside a chamber queue.
type Progress struct {
	PlanID      uuid.UUID    `json:"plan_id"`
	Entries     []QueueEntry `json:"entries"`
	Total       int          `json:"total"`
	Standby     int          `json:"standby"`
	Allowed     int          `json:"allowed"`
	Entered     int          `json:"entered"`
	Withdrawn   int          `json:"withdrawn"`
	ReadyToSeal bool         `json:"ready_to_seal"`
}

// Queue preserves FIFO order while tracking each vessel through confirmation.
type Queue struct {
	mu      sync.RWMutex
	entries map[string][]QueueEntry
}

func NewQueue() *Queue { return &Queue{entries: make(map[string][]QueueEntry)} }

func (q *Queue) Add(chamberID string, planID uuid.UUID, vessels []model.Vessel, now time.Time) error {
	if chamberID == "" || planID == uuid.Nil || len(vessels) == 0 {
		return errors.New("queue addition requires chamber, plan and vessels")
	}
	q.mu.Lock()
	next := len(q.entries[chamberID])
	for _, vessel := range vessels {
		q.entries[chamberID] = append(q.entries[chamberID], QueueEntry{
			Vessel: vessel, PlanID: planID, Sequence: next,
			State: model.AdmissionStandby, ChangedAt: now.UTC(),
		})
		next++
	}
	q.mu.Unlock()
	return nil
}

// Peek returns the active head: entered vessels are inside the chamber and no
// longer queue, while a withdrawn vessel stays at the head and blocks followers.
func (q *Queue) Peek(chamberID string) (QueueEntry, bool) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	for _, entry := range q.entries[chamberID] {
		if entry.State == model.AdmissionEntered {
			continue
		}
		return entry, true
	}
	return QueueEntry{}, false
}

// activeHeadIndex returns the first slot that has not conclusively entered:
// a withdrawn vessel stays at the head and blocks every follower.
func (q *Queue) activeHeadIndex(entries []QueueEntry, vesselID uuid.UUID) (int, error) {
	for index := range entries {
		if entries[index].State == model.AdmissionEntered {
			continue
		}
		if entries[index].Vessel.ID != vesselID {
			return index, errors.New("preceding vessel has not entered the chamber yet")
		}
		return index, nil
	}
	return -1, errors.New("vessel is not waiting in the queue")
}

// Allow moves the active standby head to allowed; followers stay held.
func (q *Queue) Allow(chamberID string, vesselID uuid.UUID, now time.Time) (QueueEntry, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	entries := q.entries[chamberID]
	index, err := q.activeHeadIndex(entries, vesselID)
	if err != nil || entries[index].State != model.AdmissionStandby {
		return QueueEntry{}, false
	}
	entries[index].State = model.AdmissionAllowed
	entries[index].Reason = ""
	entries[index].ChangedAt = now.UTC()
	return entries[index], true
}

// ConfirmEntry records that an allowed vessel is actually inside the chamber.
func (q *Queue) ConfirmEntry(chamberID string, vesselID uuid.UUID, now time.Time) (QueueEntry, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	entries := q.entries[chamberID]
	for index := range entries {
		if entries[index].Vessel.ID != vesselID {
			continue
		}
		if entries[index].State != model.AdmissionAllowed {
			return QueueEntry{}, false
		}
		entries[index].State = model.AdmissionEntered
		entries[index].ChangedAt = now.UTC()
		return entries[index], true
	}
	return QueueEntry{}, false
}

// Withdraw marks a failure or lost-contact vessel and keeps followers blocked.
func (q *Queue) Withdraw(chamberID string, vesselID uuid.UUID, reason string, now time.Time) (QueueEntry, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for index := range q.entries[chamberID] {
		entry := &q.entries[chamberID][index]
		if entry.Vessel.ID != vesselID || entry.State.Terminal() {
			continue
		}
		entry.State = model.AdmissionWithdrawn
		entry.Reason = reason
		entry.ChangedAt = now.UTC()
		return *entry, true
	}
	return QueueEntry{}, false
}

// Retry returns a withdrawn slot to standby so the vessel can be cleared again.
func (q *Queue) Retry(chamberID string, vesselID uuid.UUID, now time.Time) (QueueEntry, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	entries := q.entries[chamberID]
	index, err := q.activeHeadIndex(entries, vesselID)
	if err != nil || entries[index].State != model.AdmissionWithdrawn {
		return QueueEntry{}, false
	}
	entries[index].State = model.AdmissionStandby
	entries[index].Reason = ""
	entries[index].ChangedAt = now.UTC()
	return entries[index], true
}

// RevertAllow rolls allowed back to standby after a failed journal write.
func (q *Queue) RevertAllow(chamberID string, vesselID uuid.UUID, now time.Time) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for index := range q.entries[chamberID] {
		entry := &q.entries[chamberID][index]
		if entry.Vessel.ID == vesselID && entry.State == model.AdmissionAllowed {
			entry.State = model.AdmissionStandby
			entry.ChangedAt = now.UTC()
			return true
		}
	}
	return false
}

// RevertEntry rolls entered back to allowed after a failed journal write.
func (q *Queue) RevertEntry(chamberID string, vesselID uuid.UUID, now time.Time) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for index := range q.entries[chamberID] {
		entry := &q.entries[chamberID][index]
		if entry.Vessel.ID == vesselID && entry.State == model.AdmissionEntered {
			entry.State = model.AdmissionAllowed
			entry.ChangedAt = now.UTC()
			return true
		}
	}
	return false
}

// Find locates a single queue slot.
func (q *Queue) Find(chamberID string, vesselID uuid.UUID) (QueueEntry, bool) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	for _, entry := range q.entries[chamberID] {
		if entry.Vessel.ID == vesselID {
			return entry, true
		}
	}
	return QueueEntry{}, false
}

func (q *Queue) RemovePlan(chamberID string, planID uuid.UUID) []QueueEntry {
	q.mu.Lock()
	defer q.mu.Unlock()
	entries := q.entries[chamberID]
	kept := entries[:0]
	var removed []QueueEntry
	for _, entry := range entries {
		if entry.PlanID == planID {
			removed = append(removed, entry)
		} else {
			kept = append(kept, entry)
		}
	}
	q.entries[chamberID] = append([]QueueEntry(nil), kept...)
	return removed
}

func (q *Queue) Restore(chamberID string, entries []QueueEntry) {
	q.mu.Lock()
	current := q.entries[chamberID]
	restored := make([]QueueEntry, 0, len(entries)+len(current))
	for _, entry := range entries {
		entry.State = model.AdmissionStandby
		entry.Reason = ""
		restored = append(restored, entry)
	}
	q.entries[chamberID] = append(restored, current...)
	q.renumber(chamberID)
	q.mu.Unlock()
}

func (q *Queue) renumber(chamberID string) {
	entries := q.entries[chamberID]
	for index := range entries {
		entries[index].Sequence = index
	}
}

func (q *Queue) List(chamberID string) []QueueEntry {
	q.mu.RLock()
	result := append([]QueueEntry(nil), q.entries[chamberID]...)
	q.mu.RUnlock()
	return result
}

// PlanProgress returns the per-vessel confirmation progress for one plan.
func (q *Queue) PlanProgress(chamberID string, planID uuid.UUID) Progress {
	q.mu.RLock()
	defer q.mu.RUnlock()
	progress := Progress{PlanID: planID}
	for _, entry := range q.entries[chamberID] {
		if entry.PlanID != planID {
			continue
		}
		progress.Entries = append(progress.Entries, entry)
		progress.Total++
		switch entry.State {
		case model.AdmissionStandby:
			progress.Standby++
		case model.AdmissionAllowed:
			progress.Allowed++
		case model.AdmissionEntered:
			progress.Entered++
		case model.AdmissionWithdrawn:
			progress.Withdrawn++
		}
	}
	progress.ReadyToSeal = progress.Total > 0 && progress.Entered == progress.Total
	return progress
}

// Blocked reports why the queue head cannot advance, empty when clear.
func (q *Queue) Blocked(chamberID string) string {
	entry, ok := q.Peek(chamberID)
	if !ok {
		return "queue empty"
	}
	switch entry.State {
	case model.AdmissionWithdrawn:
		if entry.Reason != "" {
			return "head vessel withdrawn: " + entry.Reason
		}
		return "head vessel has withdrawn"
	case model.AdmissionStandby:
		return "head vessel is standing by for admission clearance"
	case model.AdmissionAllowed:
		return "head vessel has not entered the chamber yet"
	}
	return ""
}
