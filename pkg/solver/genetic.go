package solver

import (
	"context"
	"fmt"
	"geliana-go/pkg/evaluator"
	"geliana-go/pkg/models"
	"math/rand"
	"sort"
	"sync"
	"time"
)

// SchedulableItem represents a single unit of scheduling (a gene).
type SchedulableItem struct {
	LessonID     string
	DistIdx      int
	MidIdx       int
	Length       int
	Instructor   string
	Group        string
	Unit         string
	Online       bool
	AffectedGrps []string
	LessonDef    *models.Lesson
}

// GeneticSolver implements a two-phase Genetic Algorithm for timetable optimization.
type GeneticSolver struct {
	PopulationSize int
	EliteSize      int
	MutationRate   float64
	CrossoverRate  float64
	MaxGenerations int

	maxPhase1Gen int // max generations for Phase 1 clash repair
	plateauLimit int // stop after N gens without improvement

	schedulableItems []SchedulableItem
	rooms            []models.Room
	days             []string
	blocks           []models.Block

	roomMap       map[string]int
	unitMap       map[string]models.Unit
	instructorMap map[string]models.Instructor
	groupMap      map[string]models.Group

	evalParams *evaluator.EvaluationParams
}

// Gene represents the assignment for a SchedulableItem: (DayIdx, BlockIdx, RoomIdx)
type Gene struct {
	DayIdx   int
	BlockIdx int
	RoomIdx  int // -1 for online or unassigned
}

type Chromosome []Gene

func NewGeneticSolver(popSize, maxGen int) *GeneticSolver {
	return &GeneticSolver{
		PopulationSize: popSize,
		EliteSize:      max(2, popSize/10),
		MutationRate:   0.05,
		CrossoverRate:  0.8,
		MaxGenerations: maxGen,
		maxPhase1Gen:   20,
		plateauLimit:   30,
	}
}

// Solve runs the two-phase genetic algorithm.
func (s *GeneticSolver) Solve(ctx context.Context, req *models.Request, tracker ProgressTracker) (*models.Response, error) {
	startTime := time.Now()

	s.preprocess(req)
	if len(s.schedulableItems) == 0 {
		return nil, fmt.Errorf("no schedulable items found")
	}
	s.buildEvalParams()

	population := s.initializePopulation(ctx)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	var globalBest Chromosome
	globalBestFitness := -1.0
	plateauCount := 0

	phase := 1

	for gen := 0; gen < s.MaxGenerations; gen++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		// Transition from Phase 1 to Phase 2
		if gen >= s.maxPhase1Gen && phase == 1 {
			phase = 2
		}

		// --- Phase 1: targeted repair of hard clashes, no crossover ---
		if phase == 1 {
			population = s.phase1Repair(population)
		}

		// Evaluate all chromosomes using the constraint evaluator
		fitnessScores := s.evaluatePopulation(population)

		// Track best and check plateau
		currentBestIdx := 0
		for i, f := range fitnessScores {
			if f > fitnessScores[currentBestIdx] {
				currentBestIdx = i
			}
		}
		currentBestFitness := fitnessScores[currentBestIdx]

		if currentBestFitness > globalBestFitness+0.001 {
			globalBestFitness = currentBestFitness
			globalBest = make(Chromosome, len(population[currentBestIdx]))
			copy(globalBest, population[currentBestIdx])
			plateauCount = 0
		} else {
			plateauCount++
		}

		// Phase transition check: move to Phase 2 once hard clashes are eliminated
		if phase == 1 {
			hardScore := s.evaluateHard(currentBestFitness, population[currentBestIdx])
			if hardScore >= 100.0 {
				phase = 2
			}
		}

		// Progress report
		if tracker != nil {
			tracker.Update(ProgressReport{
				CurrentStep: gen + 1,
				TotalSteps:  s.MaxGenerations,
				StepLabel:   fmt.Sprintf("Gen %d [Phase %d]", gen, phase),
				BestFitness: globalBestFitness,
				Metrics: map[string]interface{}{
					"best_fitness":  globalBestFitness,
					"generation":    gen,
					"phase":         phase,
					"plateau":       plateauCount,
					"mutation_rate": s.MutationRate,
				},
				Trace: &models.TraceStep{
					Type:  "generation",
					Label: fmt.Sprintf("Generation %d (Phase %d)", gen, phase),
					Score: globalBestFitness,
				},
			})
		}

		// Early exit: optimal or stale
		if globalBestFitness >= 99.999 || plateauCount >= s.plateauLimit {
			break
		}

		// --- Phase 2: selection, crossover, mutation ---
		if phase == 2 {
			population = s.evolve(population, fitnessScores)
		}
	}

	if globalBest == nil {
		return nil, fmt.Errorf("no solution found")
	}
	return s.formatResponse(globalBest, time.Since(startTime).Seconds()), nil
}

// ---------------------------------------------------------------------------
// Initialization
// ---------------------------------------------------------------------------

func (s *GeneticSolver) initializePopulation(ctx context.Context) []Chromosome {
	population := make([]Chromosome, s.PopulationSize)
	var wg sync.WaitGroup
	wg.Add(s.PopulationSize)

	for i := 0; i < s.PopulationSize; i++ {
		go func(idx int) {
			defer wg.Done()
			population[idx] = s.initializeSmart()
		}(i)
	}
	wg.Wait()
	return population
}

// initializeSmart creates one chromosome by placing items greedily to avoid clashes.
func (s *GeneticSolver) initializeSmart() Chromosome {
	c := make(Chromosome, len(s.schedulableItems))

	type occKey struct{ day, block int }
	groupOcc := make(map[string]map[occKey]bool)
	instrOcc := make(map[string]map[occKey]bool)
	roomOcc := make(map[int]map[occKey]bool)

	for i, item := range s.schedulableItems {
		bestPenalty := -1
		bestGene := Gene{}

		// Try up to 15 random placements, keep the one with least conflicts
		for attempt := 0; attempt < 15; attempt++ {
			dayIdx := rand.Intn(len(s.days))
			maxStart := len(s.blocks) - item.Length
			if maxStart < 0 {
				maxStart = 0
			}
			blockIdx := 0
			if maxStart > 0 {
				blockIdx = rand.Intn(maxStart + 1)
			}
			roomIdx := -1
			if !item.Online && len(s.rooms) > 0 {
				roomIdx = rand.Intn(len(s.rooms))
			}

			penalty := 0
			ok := occKey{dayIdx, blockIdx}

			for _, grp := range item.AffectedGrps {
				if grp != "" && groupOcc[grp] != nil && groupOcc[grp][ok] {
					penalty++
				}
			}
			if item.Instructor != "" && instrOcc[item.Instructor] != nil && instrOcc[item.Instructor][ok] {
				penalty++
			}
			if !item.Online && roomIdx != -1 && roomOcc[roomIdx] != nil && roomOcc[roomIdx][ok] {
				penalty++
			}

			if bestPenalty == -1 || penalty < bestPenalty {
				bestPenalty = penalty
				bestGene = Gene{DayIdx: dayIdx, BlockIdx: blockIdx, RoomIdx: roomIdx}
				if penalty == 0 {
					break
				}
			}
		}

		c[i] = bestGene
		ok := occKey{bestGene.DayIdx, bestGene.BlockIdx}
		for _, grp := range item.AffectedGrps {
			if grp == "" {
				continue
			}
			if groupOcc[grp] == nil {
				groupOcc[grp] = make(map[occKey]bool)
			}
			groupOcc[grp][ok] = true
		}
		if item.Instructor != "" {
			if instrOcc[item.Instructor] == nil {
				instrOcc[item.Instructor] = make(map[occKey]bool)
			}
			instrOcc[item.Instructor][ok] = true
		}
		if !item.Online && bestGene.RoomIdx != -1 {
			if roomOcc[bestGene.RoomIdx] == nil {
				roomOcc[bestGene.RoomIdx] = make(map[occKey]bool)
			}
			roomOcc[bestGene.RoomIdx][ok] = true
		}
	}
	return c
}

// ---------------------------------------------------------------------------
// Phase 1 — Clash Repair
// ---------------------------------------------------------------------------

// phase1Repair applies targeted mutation to the worst half of the population.
func (s *GeneticSolver) phase1Repair(population []Chromosome) []Chromosome {
	// Use fast penalty-based scoring for Phase 1
	type scored struct {
		idx  int
		cost float64
	}
	scoredPop := make([]scored, len(population))
	var wg sync.WaitGroup
	wg.Add(len(population))

	for i := range population {
		go func(idx int) {
			defer wg.Done()
			scoredPop[idx] = scored{idx, s.calculatePenalty(population[idx])}
		}(i)
	}
	wg.Wait()

	sort.Slice(scoredPop, func(i, j int) bool {
		return scoredPop[i].cost < scoredPop[j].cost
	})

	// Repair the worst half
	repairCount := len(population) / 2
	for i := 0; i < repairCount; i++ {
		idx := scoredPop[len(population)-1-i].idx
		if scoredPop[len(population)-1-i].cost == 0 {
			continue
		}
		repaired := s.repairChromosome(population[idx])
		newCost := s.calculatePenalty(repaired)
		if newCost < scoredPop[len(population)-1-i].cost {
			population[idx] = repaired
		}
	}
	return population
}

// repairChromosome finds items involved in clashes and moves them to random new slots.
func (s *GeneticSolver) repairChromosome(c Chromosome) Chromosome {
	result := make(Chromosome, len(c))
	copy(result, c)

	type occKey struct{ day, block int }
	groupOcc := make(map[string][]occKey)
	instrOcc := make(map[string][]occKey)
	roomOcc := make(map[int][]occKey)

	// First pass: record all occupied slots
	for i, gene := range c {
		item := s.schedulableItems[i]
		ok := occKey{gene.DayIdx, gene.BlockIdx}

		for _, grp := range item.AffectedGrps {
			if grp != "" {
				groupOcc[grp] = append(groupOcc[grp], ok)
			}
		}
		if item.Instructor != "" {
			instrOcc[item.Instructor] = append(instrOcc[item.Instructor], ok)
		}
		if !item.Online && gene.RoomIdx != -1 {
			roomOcc[gene.RoomIdx] = append(roomOcc[gene.RoomIdx], ok)
		}
	}

	clashing := make(map[int]bool)

	for i, gene := range c {
		item := s.schedulableItems[i]
		ok := occKey{gene.DayIdx, gene.BlockIdx}

		// Count how many items share this slot for each entity
		for _, grp := range item.AffectedGrps {
			if grp == "" {
				continue
			}
			count := 0
			for _, occ := range groupOcc[grp] {
				if occ == ok {
					count++
				}
			}
			if count > 1 {
				clashing[i] = true
			}
		}
		if item.Instructor != "" {
			count := 0
			for _, occ := range instrOcc[item.Instructor] {
				if occ == ok {
					count++
				}
			}
			if count > 1 {
				clashing[i] = true
			}
		}
		if !item.Online && gene.RoomIdx != -1 {
			count := 0
			for _, occ := range roomOcc[gene.RoomIdx] {
				if occ == ok {
					count++
				}
			}
			if count > 1 {
				clashing[i] = true
			}
		}
	}

	// Move clashing items
	for i := range clashing {
		item := s.schedulableItems[i]
		result[i].DayIdx = rand.Intn(len(s.days))
		maxStart := len(s.blocks) - item.Length
		if maxStart > 0 {
			result[i].BlockIdx = rand.Intn(maxStart + 1)
		} else {
			result[i].BlockIdx = 0
		}
		if !item.Online && len(s.rooms) > 0 {
			result[i].RoomIdx = rand.Intn(len(s.rooms))
		}
	}
	return result
}

// calculatePenalty returns a fast penalty score (lower = better) for Phase 1 ranking.
func (s *GeneticSolver) calculatePenalty(c Chromosome) float64 {
	penalty := 0.0
	type slot struct{ day, start, end int }
	instSchedules := make(map[string][]slot)
	roomSchedules := make(map[int][]slot)
	groupSchedules := make(map[string][]slot)
	syncMap := make(map[string]slot)

	for i, gene := range c {
		item := s.schedulableItems[i]
		endBlock := gene.BlockIdx + item.Length

		syncKey := fmt.Sprintf("%s_%d", item.LessonID, item.DistIdx)
		if s.IsSubgroup(item.LessonDef.Type) {
			if target, ok := syncMap[syncKey]; ok {
				if gene.DayIdx != target.day || gene.BlockIdx != target.start {
					penalty += 5000
				}
			} else {
				syncMap[syncKey] = slot{gene.DayIdx, gene.BlockIdx, endBlock}
			}
		}

		for _, sc := range instSchedules[item.Instructor] {
			if sc.day == gene.DayIdx && max(gene.BlockIdx, sc.start) < min(endBlock, sc.end) {
				penalty += 1000
			}
		}
		instSchedules[item.Instructor] = append(instSchedules[item.Instructor], slot{gene.DayIdx, gene.BlockIdx, endBlock})

		if !item.Online && gene.RoomIdx != -1 {
			for _, sc := range roomSchedules[gene.RoomIdx] {
				if sc.day == gene.DayIdx && max(gene.BlockIdx, sc.start) < min(endBlock, sc.end) {
					penalty += 1000
				}
			}
			roomSchedules[gene.RoomIdx] = append(roomSchedules[gene.RoomIdx], slot{gene.DayIdx, gene.BlockIdx, endBlock})
		}

		for _, grp := range item.AffectedGrps {
			if grp == "" {
				continue
			}
			for _, sc := range groupSchedules[grp] {
				if sc.day == gene.DayIdx && max(gene.BlockIdx, sc.start) < min(endBlock, sc.end) {
					penalty += 1000
				}
			}
			groupSchedules[grp] = append(groupSchedules[grp], slot{gene.DayIdx, gene.BlockIdx, endBlock})
		}

		if !item.Online && gene.RoomIdx != -1 {
			room := s.rooms[gene.RoomIdx]
			for _, grpID := range item.AffectedGrps {
				if grpID != "" {
					if grp, ok := s.groupMap[grpID]; ok && grp.Total > room.Capacity {
						penalty += 50
					}
				}
			}
		}
	}
	return penalty
}

// ---------------------------------------------------------------------------
// Population Evaluation (Phase 2 — uses constraint evaluator)
// ---------------------------------------------------------------------------

func (s *GeneticSolver) evaluatePopulation(population []Chromosome) []float64 {
	scores := make([]float64, len(population))
	var wg sync.WaitGroup
	wg.Add(len(population))

	for i := range population {
		go func(idx int) {
			defer wg.Done()
			scores[idx] = s.evaluateFitness(population[idx])
		}(i)
	}
	wg.Wait()
	return scores
}

func (s *GeneticSolver) evaluateFitness(c Chromosome) float64 {
	sessions := s.chromosomeToSessions(c)
	result := evaluator.Evaluate(sessions, s.evalParams)
	return result.OverallScore
}

// evaluateHard extracts just the hard score from a chromosome.
func (s *GeneticSolver) evaluateHard(overall float64, c Chromosome) float64 {
	sessions := s.chromosomeToSessions(c)
	result := evaluator.Evaluate(sessions, s.evalParams)
	return result.HardScore
}

// ---------------------------------------------------------------------------
// Chromosome <-> Sessions conversion
// ---------------------------------------------------------------------------

func (s *GeneticSolver) chromosomeToSessions(c Chromosome) []models.Session {
	type sessionKey struct {
		lessonID string
		distIdx  int
	}
	merged := make(map[sessionKey]models.Session)

	for i, gene := range c {
		item := s.schedulableItems[i]
		key := sessionKey{item.LessonID, item.DistIdx}
		roomID := ""
		if gene.RoomIdx != -1 {
			roomID = s.rooms[gene.RoomIdx].ID
		}
		if sess, ok := merged[key]; ok {
			if roomID != "" && sess.Room != roomID {
				sess.Room += ", " + roomID
			}
			merged[key] = sess
		} else {
			dbIDs := item.LessonDef.DatabaseIDs
			if !item.Online && gene.RoomIdx != -1 {
				dbIDs.Room = s.rooms[gene.RoomIdx].DatabaseID
			}
			merged[key] = models.Session{
				Identifier:  fmt.Sprintf("%s-%d", item.LessonID, item.DistIdx),
				Group:       item.Group,
				Instructor:  item.Instructor,
				Unit:        item.Unit,
				Day:         s.days[gene.DayIdx],
				Time:        s.blocks[gene.BlockIdx].Start,
				Room:        roomID,
				Online:      item.Online,
				Blocks:      item.Length,
				DatabaseIDs: dbIDs,
				TimetableID: item.LessonDef.TimetableID,
			}
		}
	}

	sessions := make([]models.Session, 0, len(merged))
	for _, sess := range merged {
		sessions = append(sessions, sess)
	}
	return sessions
}

// ---------------------------------------------------------------------------
// Evolution (Phase 2)
// ---------------------------------------------------------------------------

func (s *GeneticSolver) evolve(population []Chromosome, scores []float64) []Chromosome {
	newPop := make([]Chromosome, 0, s.PopulationSize)

	// Elitism
	indices := make([]int, len(population))
	for i := range indices {
		indices[i] = i
	}
	sort.Slice(indices, func(i, j int) bool {
		return scores[indices[i]] > scores[indices[j]]
	})

	for i := 0; i < s.EliteSize; i++ {
		elite := make(Chromosome, len(population[indices[i]]))
		copy(elite, population[indices[i]])
		newPop = append(newPop, elite)
	}

	// Reproduction
	for len(newPop) < s.PopulationSize {
		p1 := s.tournamentSelect(population, scores)
		p2 := s.tournamentSelect(population, scores)

		var c1, c2 Chromosome
		if rand.Float64() < s.CrossoverRate {
			c1, c2 = s.twoPointCrossover(p1, p2)
		} else {
			c1, c2 = make(Chromosome, len(p1)), make(Chromosome, len(p2))
			copy(c1, p1)
			copy(c2, p2)
		}

		s.mutate(c1)
		s.mutate(c2)

		newPop = append(newPop, c1)
		if len(newPop) < s.PopulationSize {
			newPop = append(newPop, c2)
		}
	}

	return newPop
}

func (s *GeneticSolver) tournamentSelect(pop []Chromosome, scores []float64) Chromosome {
	k := 5
	best := -1
	for i := 0; i < k; i++ {
		idx := rand.Intn(len(pop))
		if best == -1 || scores[idx] > scores[best] {
			best = idx
		}
	}
	return pop[best]
}

// twoPointCrossover swaps a contiguous segment between two parents.
func (s *GeneticSolver) twoPointCrossover(p1, p2 Chromosome) (Chromosome, Chromosome) {
	n := len(p1)
	pt1 := rand.Intn(n)
	pt2 := rand.Intn(n)
	if pt1 > pt2 {
		pt1, pt2 = pt2, pt1
	}

	c1 := make(Chromosome, n)
	c2 := make(Chromosome, n)

	for i := 0; i < n; i++ {
		if i >= pt1 && i < pt2 {
			c1[i], c2[i] = p2[i], p1[i]
		} else {
			c1[i], c2[i] = p1[i], p2[i]
		}
	}
	return c1, c2
}

func (s *GeneticSolver) mutate(c Chromosome) {
	// Random mutation: 5% of genes get random new slots
	for i := range c {
		if rand.Float64() < s.MutationRate {
			item := s.schedulableItems[i]
			c[i].DayIdx = rand.Intn(len(s.days))
			maxStart := len(s.blocks) - item.Length
			if maxStart > 0 {
				c[i].BlockIdx = rand.Intn(maxStart + 1)
			} else {
				c[i].BlockIdx = 0
			}
			if !item.Online && len(s.rooms) > 0 {
				c[i].RoomIdx = rand.Intn(len(s.rooms))
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Output
// ---------------------------------------------------------------------------

func (s *GeneticSolver) formatResponse(c Chromosome, runtime float64) *models.Response {
	sessions := s.chromosomeToSessions(c)

	evalResult := evaluator.Evaluate(sessions, s.evalParams)

	return &models.Response{
		Error:    false,
		Message:  "Optimization successful",
		Sessions: sessions,
		Stats: models.OptimizationStats{
			OverallScore:  evalResult.OverallScore,
			TimeTaken:     runtime,
			SolutionFound: true,
		},
	}
}

// ---------------------------------------------------------------------------
// Preprocessing
// ---------------------------------------------------------------------------

func (s *GeneticSolver) preprocess(req *models.Request) {
	s.rooms = req.Rooms
	s.days = req.Days
	s.blocks = req.Blocks

	s.roomMap = make(map[string]int)
	for i, r := range s.rooms {
		s.roomMap[r.ID] = i
	}

	s.unitMap = make(map[string]models.Unit)
	for _, u := range req.Units {
		s.unitMap[u.ID] = u
	}

	s.instructorMap = make(map[string]models.Instructor)
	for _, i := range req.Instructors {
		s.instructorMap[i.ID] = i
	}

	s.groupMap = make(map[string]models.Group)
	for _, g := range req.Groups {
		s.groupMap[g.ID] = g
	}

	s.schedulableItems = nil
	for _, lesson := range req.Lessons {
		l := lesson
		for distIdx, count := range l.Distribution {
			if count == 0 {
				continue
			}

			if l.Type == "regular" {
				s.schedulableItems = append(s.schedulableItems, SchedulableItem{
					LessonID:     l.Identifier,
					DistIdx:      distIdx,
					Length:       count,
					Instructor:   l.Instructor,
					Group:        l.Group,
					Unit:         l.Unit,
					Online:       l.Online,
					AffectedGrps: []string{l.Group},
					LessonDef:    &l,
				})
			} else if l.Type == "lesson-merge" {
				var affected []string
				for _, m := range l.MultipleIDs {
					affected = append(affected, m.GroupID)
				}
				s.schedulableItems = append(s.schedulableItems, SchedulableItem{
					LessonID:     l.Identifier,
					DistIdx:      distIdx,
					Length:       count,
					Instructor:   l.Instructor,
					Group:        "",
					Unit:         l.Unit,
					Online:       l.Online,
					AffectedGrps: affected,
					LessonDef:    &l,
				})
			} else if l.Type == "subgroup" || l.Type == "subgroup-lesson-merge" {
				for midIdx, m := range l.MultipleIDs {
					s.schedulableItems = append(s.schedulableItems, SchedulableItem{
						LessonID:     l.Identifier,
						DistIdx:      distIdx,
						MidIdx:       midIdx,
						Length:       count,
						Instructor:   m.InstID,
						Group:        m.GroupID,
						Unit:         m.UnitID,
						Online:       l.Online,
						AffectedGrps: m.AffectedGroups,
						LessonDef:    &l,
					})
				}
			}
		}
	}
}

func (s *GeneticSolver) buildEvalParams() {
	periodTimes := make([]string, len(s.blocks))
	for i, b := range s.blocks {
		periodTimes[i] = b.Start
	}

	lessonSet := make(map[string]models.Lesson)
	for _, item := range s.schedulableItems {
		if item.LessonDef != nil {
			lessonSet[item.LessonDef.Identifier] = *item.LessonDef
		}
	}
	lessons := make([]models.Lesson, 0, len(lessonSet))
	for _, l := range lessonSet {
		lessons = append(lessons, l)
	}

	groups := make([]models.Group, 0, len(s.groupMap))
	for _, g := range s.groupMap {
		groups = append(groups, g)
	}
	units := make([]models.Unit, 0, len(s.unitMap))
	for _, u := range s.unitMap {
		units = append(units, u)
	}
	instructors := make([]models.Instructor, 0, len(s.instructorMap))
	for _, i := range s.instructorMap {
		instructors = append(instructors, i)
	}

	s.evalParams = &evaluator.EvaluationParams{
		Days:        s.days,
		PeriodTimes: periodTimes,
		Lessons:     lessons,
		Rooms:       s.rooms,
		Groups:      groups,
		Instructors: instructors,
		Units:       units,
		Blocks:      s.blocks,
	}
}

func (s *GeneticSolver) IsSubgroup(t string) bool {
	return t == "subgroup" || t == "subgroup-lesson-merge"
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
