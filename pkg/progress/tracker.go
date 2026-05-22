package progress

import (
	"geliana-go/pkg/models"
	"geliana-go/pkg/solver"
	"sync"
	"time"
)

// DefaultTracker implements the solver.ProgressTracker interface.
type DefaultTracker struct {
	mu            sync.RWMutex
	startTime     time.Time
	totalSteps    int
	currentReport solver.ProgressReport
	onUpdate      func(solver.ProgressReport)
	trace         []models.TraceStep
}

// NewDefaultTracker creates a new tracker with a start time and total steps.
func NewDefaultTracker(totalSteps int, onUpdate func(solver.ProgressReport)) *DefaultTracker {
	return &DefaultTracker{
		startTime:  time.Now(),
		totalSteps: totalSteps,
		onUpdate:   onUpdate,
		trace:      make([]models.TraceStep, 0),
	}
}

// Update calculates the ETA and updates the current progress report.
func (t *DefaultTracker) Update(report solver.ProgressReport) {
	t.mu.Lock()
	defer t.mu.Unlock()

	elapsed := time.Since(t.startTime).Seconds()
	
	// Handle Trace Step
	if report.Trace != nil {
		report.Trace.Step = len(t.trace) + 1
		report.Trace.Timestamp = elapsed
		t.trace = append(t.trace, *report.Trace)
	}

	// Set total steps if not already set or if updated by the solver
	if report.TotalSteps > 0 {
		t.totalSteps = report.TotalSteps
	}
	report.TotalSteps = t.totalSteps

	// Calculate ETA
	if report.CurrentStep > 0 && elapsed > 0 {
		rate := float64(report.CurrentStep) / elapsed
		remainingSteps := float64(t.totalSteps - report.CurrentStep)
		if remainingSteps > 0 {
			report.ETASeconds = remainingSteps / rate
		} else {
			report.ETASeconds = 0
		}
	}

	t.currentReport = report

	if t.onUpdate != nil {
		t.onUpdate(report)
	}
}

// GetCurrentReport returns the last reported progress.
func (t *DefaultTracker) GetCurrentReport() solver.ProgressReport {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.currentReport
}

// GetTrace returns the accumulated search path.
func (t *DefaultTracker) GetTrace() []models.TraceStep {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.trace
}

// ElapsedSeconds returns the time since the tracker was started.
func (t *DefaultTracker) ElapsedSeconds() float64 {
	return time.Since(t.startTime).Seconds()
}
