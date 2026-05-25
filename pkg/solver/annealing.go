package solver

import (
	"context"
	"fmt"
	"geliana-go/pkg/evaluator"
	"geliana-go/pkg/models"
	"math"
	"math/rand"
	"time"
)

// SimulatedAnnealingSolver implements Simulated Annealing for timetable
// optimisation. Key design decisions:
//
//   - Smart greedy initialisation (same logic as GeneticSolver) so the
//     starting solution is already 60–80% clash-free.
//   - Two-mode neighbour generation: targeted (move a clashing gene) and
//     random (escape local optima). Ratio shifts as temperature drops.
//   - Full constraint evaluator used for scoring so SA optimises the same
//     objective as the GA — preferences, distribution, and rooms included.
//   - Adaptive reheating: if acceptance rate drops below a threshold the
//     temperature is bumped back up, preventing premature freezing.
//   - Shared helpers (preprocess, chromosomeToSessions, buildEvalParams)
//     delegated to a GeneticSolver instance to avoid duplication.
type SimulatedAnnealingSolver struct {
	// Cooling schedule parameters.
	InitialTemp float64
	CoolingRate float64
	MinTemp     float64
	MaxIter     int

	// Reheating: if acceptance rate over the last reheatWindow iterations
	// falls below reheatThreshold, temperature is multiplied by reheatFactor.
	reheatWindow    int
	reheatThreshold float64
	reheatFactor    float64

	// Shared state — populated by preprocess via an embedded GeneticSolver.
	gs *GeneticSolver
}

func NewSimulatedAnnealingSolver(iter int) *SimulatedAnnealingSolver {
	return &SimulatedAnnealingSolver{
		InitialTemp:     1000.0,
		CoolingRate:     0.995,
		MinTemp:         0.1,
		MaxIter:         iter,
		reheatWindow:    200,
		reheatThreshold: 0.02,  // reheat if fewer than 2% of moves accepted
		reheatFactor:    2.0,
	}
}

// ---------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------

func (s *SimulatedAnnealingSolver) Solve(
	ctx context.Context,
	req *models.Request,
	tracker ProgressTracker,
) (*models.Response, error) {
	startTime := time.Now()

	// Delegate all preprocessing to GeneticSolver so nothing is duplicated.
	s.gs = NewGeneticSolver(1, 1) // population/gen params unused here
	s.gs.preprocess(req)
	s.gs.buildEvalParams()

	if len(s.gs.schedulableItems) == 0 {
		return nil, fmt.Errorf("no schedulable items found")
	}

	// --- Phase 0: smart greedy initialisation ---
	// Uses the same bestPlacement logic as GeneticSolver.initializeSmart so
	// the starting solution is already mostly conflict-free.
	current := s.gs.initializeSmart()
	currentScore := s.fullScore(current)

	best := make(Chromosome, len(current))
	copy(best, current)
	bestScore := currentScore

	temp := s.InitialTemp

	// Rolling window for adaptive reheating.
	recentAccepted := 0
	recentAttempted := 0

	for iter := 0; iter < s.MaxIter && temp > s.MinTemp; iter++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		// --- Generate neighbour ---
		// At high temperature: explore randomly (escape basins).
		// At low temperature: target clashing genes (exploit local structure).
		targetedProb := 1.0 - (temp / s.InitialTemp) // 0 → 1 as temp cools
		var neighbour Chromosome
		if rand.Float64() < targetedProb {
			neighbour = s.targetedNeighbour(current)
		} else {
			neighbour = s.randomNeighbour(current)
		}

		neighbourScore := s.fullScore(neighbour)

		// --- Acceptance ---
		// SA accepts improvements always; accepts degradations with
		// probability exp(Δ/T). Score is 0–100 (higher = better), so
		// delta is positive when the neighbour is better.
		delta := neighbourScore - currentScore
		recentAttempted++

		if delta > 0 || rand.Float64() < math.Exp(delta/temp) {
			current = neighbour
			currentScore = neighbourScore
			recentAccepted++

			if currentScore > bestScore {
				bestScore = currentScore
				copy(best, current)
			}
		}

		// --- Cool ---
		temp *= s.CoolingRate

		// --- Adaptive reheating ---
		if recentAttempted >= s.reheatWindow {
			rate := float64(recentAccepted) / float64(recentAttempted)
			if rate < s.reheatThreshold && temp > s.MinTemp*10 {
				temp = math.Min(temp*s.reheatFactor, s.InitialTemp*0.5)
			}
			recentAccepted = 0
			recentAttempted = 0
		}

		// --- Progress report (throttled to every 100 iterations) ---
		if tracker != nil && iter%100 == 0 {
			acceptanceRate := 0.0
			if recentAttempted > 0 {
				acceptanceRate = float64(recentAccepted) / float64(recentAttempted)
			}
			tracker.Update(ProgressReport{
				CurrentStep: iter + 1,
				TotalSteps:  s.MaxIter,
				StepLabel:   fmt.Sprintf("Iteration %d", iter+1),
				BestFitness: bestScore,
				Metrics: map[string]interface{}{
					"best_score":      bestScore,
					"current_score":   currentScore,
					"temperature":     temp,
					"acceptance_rate": acceptanceRate,
					"iteration":       iter,
				},
				Trace: &models.TraceStep{
					Type:  "iteration",
					Label: fmt.Sprintf("Iteration %d", iter+1),
					Score: bestScore,
				},
			})
		}

		// Perfect solution — no point continuing.
		if bestScore >= 99.999 {
			break
		}
	}

	return s.formatResponse(best, time.Since(startTime).Seconds()), nil
}

// ---------------------------------------------------------------------------
// Scoring
// ---------------------------------------------------------------------------

// fullScore converts a chromosome to sessions and runs the full constraint
// evaluator, returning the overall score (0–100, higher is better).
// This ensures SA optimises the exact same objective as the GA.
func (s *SimulatedAnnealingSolver) fullScore(c Chromosome) float64 {
	sessions := s.gs.chromosomeToSessions(c)
	result := evaluator.Evaluate(sessions, s.gs.evalParams)
	return result.OverallScore
}

// ---------------------------------------------------------------------------
// Neighbour generation
// ---------------------------------------------------------------------------

// targetedNeighbour finds genes involved in clashes and moves one of them
// to a better slot using greedy placement. Falls back to randomNeighbour
// if no clashes are detected (solution is already clash-free).
func (s *SimulatedAnnealingSolver) targetedNeighbour(current Chromosome) Chromosome {
	clashing := s.gs.findClashingGenes(current)

	if len(clashing) == 0 {
		// No hard clashes — perturb a preference or distribution violator
		// by picking a random gene and moving it to a greedily chosen slot.
		return s.randomNeighbour(current)
	}

	// Collect clashing indices and pick one at random.
	indices := make([]int, 0, len(clashing))
	for idx := range clashing {
		indices = append(indices, idx)
	}
	pick := indices[rand.Intn(len(indices))]

	neighbour := make(Chromosome, len(current))
	copy(neighbour, current)

	// Build occupancy excluding the chosen gene so it doesn't clash
	// with itself during placement search.
	occ := newOccupancyMaps()
	for i, gene := range current {
		if i != pick {
			occ.add(&s.gs.schedulableItems[i], gene)
		}
	}

	item := &s.gs.schedulableItems[pick]
	neighbour[pick] = s.gs.bestPlacement(item, occ, 25)

	return neighbour
}

// randomNeighbour creates a neighbour by making one of three perturbations
// to a randomly selected gene:
//
//	60% — change day and time slot
//	20% — change room only (keeps time, may fix a room clash)
//	20% — swap two genes (preserves slot diversity)
func (s *SimulatedAnnealingSolver) randomNeighbour(current Chromosome) Chromosome {
	neighbour := make(Chromosome, len(current))
	copy(neighbour, current)

	n := len(neighbour)
	idx := rand.Intn(n)
	item := &s.gs.schedulableItems[idx]

	move := rand.Float64()

	switch {
	case move < 0.60:
		// New random day + block.
		maxBlock := len(s.gs.blocks) - item.Length
		if maxBlock < 0 {
			maxBlock = 0
		}
		neighbour[idx].DayIdx = rand.Intn(len(s.gs.days))
		if maxBlock > 0 {
			neighbour[idx].BlockIdx = rand.Intn(maxBlock + 1)
		} else {
			neighbour[idx].BlockIdx = 0
		}

	case move < 0.80:
		// Room swap (only meaningful for non-online sessions).
		if !item.Online && len(s.gs.rooms) > 0 {
			neighbour[idx].RoomIdx = rand.Intn(len(s.gs.rooms))
		} else {
			// Fall back to time change.
			maxBlock := len(s.gs.blocks) - item.Length
			if maxBlock < 0 {
				maxBlock = 0
			}
			neighbour[idx].DayIdx = rand.Intn(len(s.gs.days))
			if maxBlock > 0 {
				neighbour[idx].BlockIdx = rand.Intn(maxBlock + 1)
			}
		}

	default:
		// Swap two genes — preserves the set of slots, changes which
		// items occupy them. Useful when the issue is not the slot itself
		// but which lesson is in it.
		other := rand.Intn(n)
		if other != idx {
			neighbour[idx], neighbour[other] = neighbour[other], neighbour[idx]
		} else {
			// Degenerate case: just do a time change.
			maxBlock := len(s.gs.blocks) - item.Length
			if maxBlock < 0 {
				maxBlock = 0
			}
			neighbour[idx].DayIdx = rand.Intn(len(s.gs.days))
			if maxBlock > 0 {
				neighbour[idx].BlockIdx = rand.Intn(maxBlock + 1)
			}
		}
	}

	return neighbour
}

// ---------------------------------------------------------------------------
// Output
// ---------------------------------------------------------------------------

func (s *SimulatedAnnealingSolver) formatResponse(c Chromosome, runtime float64) *models.Response {
	sessions := s.gs.chromosomeToSessions(c)
	evalResult := evaluator.Evaluate(sessions, s.gs.evalParams)

	return &models.Response{
		Error:    false,
		Message:  "Simulated Annealing optimisation complete",
		Sessions: sessions,
		Stats: models.OptimizationStats{
			OverallScore:  evalResult.OverallScore,
			TimeTaken:     runtime,
			SolutionFound: evalResult.HardScore >= 100,
		},
	}
}