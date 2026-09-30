package cycle

import (
	"context"

	"github.com/wyw14/cry-96/internal/model"
)

// Service drives an operator-visible transit through its safe boundaries.
type Service struct {
	coordinator *Coordinator
}

func NewService(coordinator *Coordinator) *Service {
	return &Service{coordinator: coordinator}
}

// Start opens a transit generation. Vessels are then admitted one at a time
// through the admission API rather than being released wholesale here.
func (s *Service) Start(ctx context.Context, plan model.ConvoyPlan) (model.TransitCycle, error) {
	return s.coordinator.Start(ctx, plan)
}

func (s *Service) Current(chamberID string) (model.TransitCycle, bool) {
	return s.coordinator.Current(chamberID)
}
