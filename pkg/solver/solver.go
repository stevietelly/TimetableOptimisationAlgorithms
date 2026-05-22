package solver

import (
	"context"
	"geliana-go/pkg/models"
)

// ProgressReport represents the current state of the optimization.
type ProgressReport struct {
	CurrentStep  int                    `json:"current_step"`
	TotalSteps   int                    `json:"total_steps"`
	StepLabel    string                 `json:"step_label"`
	BestFitness  float64                `json:"best_fitness"`
	ETASeconds   float64                `json:"eta_seconds"`
	Metrics      map[string]interface{} `json:"metrics"`
	Trace        *models.TraceStep      `json:"trace,omitempty"` // New search step
}

// ProgressTracker is an interface for reporting progress back to the caller.
type ProgressTracker interface {
	Update(report ProgressReport)
}

// Solver defines the contract for optimization algorithms.
type Solver interface {
	// Solve runs the optimization algorithm.
	// It must respect the context's deadline and cancellation.
	Solve(ctx context.Context, request *models.Request, tracker ProgressTracker) (*models.Response, error)
}
