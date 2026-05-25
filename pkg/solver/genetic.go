package solver

import (
	"context"
	"fmt"
	"geliana-go/pkg/evaluator"
	"geliana-go/pkg/models"
	"math"
	"math/rand"
	"sort"
	"strings"
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

// occKey identifies one occupied block on one day.
type occKey struct{ day, block int }

// occupancyMaps holds conflict-detection state during placement and repair.
type occupancyMaps struct {
	group map[string]map[occKey]bool
	instr map[string]map[occKey]bool
	room  map[int]map[occKey]bool
}

func newOccupancyMaps() *occupancyMaps {
	return &occupancyMaps{
		group: make(map[string]map[occKey]bool),
		instr: make(map[string]map[occKey]bool),
		room:  make(map[int]map[occKey]bool),
	}
}

// add registers all blocks spanned by the gene for the given item.
func (o *occupancyMaps) add(item *SchedulableItem, gene Gene) {
	for b := gene.BlockIdx; b < gene.BlockIdx+item.Length; b++ {
		k := occKey{gene.DayIdx, b}
		for _, grp := range item.AffectedGrps {
			if grp == "" {
				continue
			}
			if o.group[grp] == nil {
				o.group[grp] = make(map[occKey]bool)
			}
			o.group[grp][k] = true
		}
		if item.Instructor != "" {
			if o.instr[item.Instructor] == nil {
				o.instr[item.Instructor] = make(map[occKey]bool)
			}
			o.instr[item.Instructor][k] = true
		}
		if !item.Online && gene.RoomIdx != -1 {
			if o.room[gene.RoomIdx] == nil {
				o.room[gene.RoomIdx] = make(map[occKey]bool)
			}
			o.room[gene.RoomIdx][k] = true
		}
	}
}

// remove clears all blocks spanned by the gene for the given item.
func (o *occupancyMaps) remove(item *SchedulableItem, gene Gene) {
	for b := gene.BlockIdx; b < gene.BlockIdx+item.Length; b++ {
		k := occKey{gene.DayIdx, b}
		for _, grp := range item.AffectedGrps {
			if grp != "" && o.group[grp] != nil {
				delete(o.group[grp], k)
			}
		}
		if item.Instructor != "" && o.instr[item.Instructor] != nil {
			delete(o.instr[item.Instructor], k)
		}
		if !item.Online && gene.RoomIdx != -1 && o.room[gene.RoomIdx] != nil {
			delete(o.room[gene.RoomIdx], k)
		}
	}
}

// conflictCount returns the number of occupied slots that collide with a
// candidate placement. It does NOT include the item itself (call remove first).
func (o *occupancyMaps) conflictCount(item *SchedulableItem, dayIdx, blockIdx, roomIdx int) int {
	penalty := 0
	for b := blockIdx; b < blockIdx+item.Length; b++ {
		k := occKey{dayIdx, b}
		for _, grp := range item.AffectedGrps {
			if grp != "" && o.group[grp] != nil && o.group[grp][k] {
				penalty++
			}
		}
		if item.Instructor != "" && o.instr[item.Instructor] != nil && o.instr[item.Instructor][k] {
			penalty++
		}
		if !item.Online && roomIdx != -1 && o.room[roomIdx] != nil && o.room[roomIdx][k] {
			penalty++
		}
	}
	return penalty
}

// bestPlacement tries `attempts` random slots and returns the one with the
// fewest conflicts. The item must already be removed from occ before calling.
func (s *GeneticSolver) bestPlacement(item *SchedulableItem, occ *occupancyMaps, attempts int) Gene {
	bestPenalty := math.MaxInt32
	best := Gene{RoomIdx: -1}

	maxBlock := len(s.blocks) - item.Length
	if maxBlock < 0 {
		maxBlock = 0
	}

	for attempt := 0; attempt < attempts; attempt++ {
		dayIdx := rand.Intn(len(s.days))
		blockIdx := 0
		if maxBlock > 0 {
			blockIdx = rand.Intn(maxBlock + 1)
		}
		roomIdx := -1
		if !item.Online && len(s.rooms) > 0 {
			roomIdx = rand.Intn(len(s.rooms))
		}

		penalty := occ.conflictCount(item, dayIdx, blockIdx, roomIdx)

		// Also penalise capacity violations (soft, but worth avoiding early)
		if !item.Online && roomIdx != -1 {
			room := s.rooms[roomIdx]
			for _, grpID := range item.AffectedGrps {
				if grpID != "" {
					if grp, ok := s.groupMap[grpID]; ok && grp.Total > room.Capacity {
						penalty++
					}
				}
			}
		}

		if penalty < bestPenalty {
			bestPenalty = penalty
			best = Gene{DayIdx: dayIdx, BlockIdx: blockIdx, RoomIdx: roomIdx}
			if bestPenalty == 0 {
				break
			}
		}
	}
	return best
}

// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Solve — main entry point
// ---------------------------------------------------------------------------

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

	// Evaluate population scores once; we reuse them across the loop.
	fitnessScores := s.evaluatePopulation(population)

	for gen := 0; gen < s.MaxGenerations; gen++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		// --- Phase 1: evaluate first, repair worst half, re-evaluate ---
		if phase == 1 {
			population, fitnessScores = s.phase1Step(population, fitnessScores)

			// Check whether hard clashes are gone in the best chromosome.
			currentBestIdx := argmax(fitnessScores)
			if s.hardScore(population[currentBestIdx]) >= 100.0 {
				phase = 2
			}

			// Force Phase 2 after maxPhase1Gen regardless.
			if gen >= s.maxPhase1Gen {
				phase = 2
			}
		} else {
			// --- Phase 2: evolve then evaluate ---
			population = s.evolve(population, fitnessScores)
			fitnessScores = s.evaluatePopulation(population)
		}

		// Track global best and plateau.
		currentBestIdx := argmax(fitnessScores)
		currentBestFitness := fitnessScores[currentBestIdx]

		if currentBestFitness > globalBestFitness+0.001 {
			globalBestFitness = currentBestFitness
			globalBest = make(Chromosome, len(population[currentBestIdx]))
			copy(globalBest, population[currentBestIdx])
			plateauCount = 0
		} else {
			plateauCount++
		}

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

		if globalBestFitness >= 99.999 || plateauCount >= s.plateauLimit {
			break
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

// initializeSmart builds one chromosome using greedy conflict-aware placement.
// Items are placed one at a time; each placement tries 15 random slots and
// keeps the one with the fewest conflicts against already-placed items.
func (s *GeneticSolver) initializeSmart() Chromosome {
	c := make(Chromosome, len(s.schedulableItems))
	occ := newOccupancyMaps()

	for i := range s.schedulableItems {
		item := &s.schedulableItems[i]
		gene := s.bestPlacement(item, occ, 15)
		c[i] = gene
		occ.add(item, gene)
	}
	return c
}

// ---------------------------------------------------------------------------
// Phase 1 — evaluate → repair → re-evaluate
// ---------------------------------------------------------------------------

// phase1Step scores the population with the fast penalty function, repairs the
// worst half using greedy slot-finding, then re-evaluates with the full evaluator.
func (s *GeneticSolver) phase1Step(population []Chromosome, prevScores []float64) ([]Chromosome, []float64) {
	// Score with fast penalty (lower = worse clash situation).
	type scored struct {
		idx  int
		cost float64
	}
	penalties := make([]scored, len(population))
	var wg sync.WaitGroup
	wg.Add(len(population))
	for i := range population {
		go func(idx int) {
			defer wg.Done()
			penalties[idx] = scored{idx, s.calculatePenalty(population[idx])}
		}(i)
	}
	wg.Wait()

	sort.Slice(penalties, func(i, j int) bool {
		return penalties[i].cost < penalties[j].cost
	})

	// Repair the worst half — only if they actually have clashes.
	repairCount := len(population) / 2
	for i := 0; i < repairCount; i++ {
		entry := penalties[len(population)-1-i]
		if entry.cost == 0 {
			continue
		}
		repaired := s.repairChromosome(population[entry.idx])
		if s.calculatePenalty(repaired) < entry.cost {
			population[entry.idx] = repaired
		}
	}

	// Re-evaluate the full population with the constraint evaluator.
	return population, s.evaluatePopulation(population)
}

// repairChromosome identifies every gene involved in a clash and re-places it
// using the greedy best-of-N approach, updating occupancy maps as it goes so
// each successive repair sees the current state of the chromosome.
func (s *GeneticSolver) repairChromosome(c Chromosome) Chromosome {
	result := make(Chromosome, len(c))
	copy(result, c)

	// Build occupancy from the current chromosome state — multi-block aware.
	occ := newOccupancyMaps()
	for i, gene := range result {
		occ.add(&s.schedulableItems[i], gene)
	}

	// Identify clashing genes (any block they occupy is also occupied by
	// another item for the same instructor, group, or room).
	clashing := s.findClashingGenes(result)

	// Re-place each clashing gene using greedy placement.
	// Remove the gene from occ first so it doesn't conflict with itself.
	for idx := range clashing {
		item := &s.schedulableItems[idx]

		// Remove old placement from occupancy.
		occ.remove(item, result[idx])

		// Find a better slot now that this gene is absent from occ.
		newGene := s.bestPlacement(item, occ, 25)
		result[idx] = newGene

		// Register the new placement so subsequent genes see it.
		occ.add(item, newGene)
	}

	return result
}

// findClashingGenes returns a set of gene indices that are involved in any
// instructor, group, or room clash. Uses interval overlap detection so that
// multi-block lessons are handled correctly.
func (s *GeneticSolver) findClashingGenes(c Chromosome) map[int]bool {
	type interval struct {
		geneIdx    int
		day, start int
		end        int
	}

	instrIntervals := make(map[string][]interval)
	groupIntervals := make(map[string][]interval)
	roomIntervals  := make(map[int][]interval)

	for i, gene := range c {
		item := &s.schedulableItems[i]
		iv := interval{i, gene.DayIdx, gene.BlockIdx, gene.BlockIdx + item.Length}

		if item.Instructor != "" {
			instrIntervals[item.Instructor] = append(instrIntervals[item.Instructor], iv)
		}
		for _, grp := range item.AffectedGrps {
			if grp != "" {
				groupIntervals[grp] = append(groupIntervals[grp], iv)
			}
		}
		if !item.Online && gene.RoomIdx != -1 {
			roomIntervals[gene.RoomIdx] = append(roomIntervals[gene.RoomIdx], iv)
		}
	}

	clashing := make(map[int]bool)

	checkOverlaps := func(ivs []interval) {
		for a := 0; a < len(ivs); a++ {
			for b := a + 1; b < len(ivs); b++ {
				if ivs[a].day == ivs[b].day &&
					max(ivs[a].start, ivs[b].start) < min(ivs[a].end, ivs[b].end) {
					clashing[ivs[a].geneIdx] = true
					clashing[ivs[b].geneIdx] = true
				}
			}
		}
	}

	for _, ivs := range instrIntervals {
		checkOverlaps(ivs)
	}
	for _, ivs := range groupIntervals {
		checkOverlaps(ivs)
	}
	for _, ivs := range roomIntervals {
		checkOverlaps(ivs)
	}

	return clashing
}

// calculatePenalty returns a fast numeric penalty for Phase 1 ranking.
// Lower is better. Uses interval overlap for accurate multi-block detection.
func (s *GeneticSolver) calculatePenalty(c Chromosome) float64 {
	penalty := 0.0

	type interval struct{ day, start, end int }
	instSlots  := make(map[string][]interval)
	roomSlots  := make(map[int][]interval)
	groupSlots := make(map[string][]interval)
	syncMap    := make(map[string]interval)

	overlap := func(a, b interval) bool {
		return a.day == b.day && max(a.start, b.start) < min(a.end, b.end)
	}

	for i, gene := range c {
		item := &s.schedulableItems[i]
		iv := interval{gene.DayIdx, gene.BlockIdx, gene.BlockIdx + item.Length}

		// Subgroup synchronisation: all sub-divisions of the same lesson+dist
		// must share the same slot.
		if s.IsSubgroup(item.LessonDef.Type) {
			syncKey := fmt.Sprintf("%s_%d", item.LessonID, item.DistIdx)
			if target, ok := syncMap[syncKey]; ok {
				if iv.day != target.day || iv.start != target.start {
					penalty += 5000
				}
			} else {
				syncMap[syncKey] = iv
			}
		}

		for _, sc := range instSlots[item.Instructor] {
			if overlap(iv, sc) {
				penalty += 1000
			}
		}
		instSlots[item.Instructor] = append(instSlots[item.Instructor], iv)

		if !item.Online && gene.RoomIdx != -1 {
			for _, sc := range roomSlots[gene.RoomIdx] {
				if overlap(iv, sc) {
					penalty += 1000
				}
			}
			roomSlots[gene.RoomIdx] = append(roomSlots[gene.RoomIdx], iv)
		}

		for _, grp := range item.AffectedGrps {
			if grp == "" {
				continue
			}
			for _, sc := range groupSlots[grp] {
				if overlap(iv, sc) {
					penalty += 1000
				}
			}
			groupSlots[grp] = append(groupSlots[grp], iv)
		}

		// Capacity soft penalty.
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
// Full evaluator (Phase 2)
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

// hardScore runs the full evaluator and returns only the hard-constraint score.
// Called sparingly — only to check phase transition in Phase 1.
func (s *GeneticSolver) hardScore(c Chromosome) float64 {
	sessions := s.chromosomeToSessions(c)
	result := evaluator.Evaluate(sessions, s.evalParams)
	return result.HardScore
}

// ---------------------------------------------------------------------------
// Chromosome ↔ Sessions
// ---------------------------------------------------------------------------

func (s *GeneticSolver) chromosomeToSessions(c Chromosome) []models.Session {
	type sessionKey struct {
		lessonID string
		distIdx  int
	}
	merged := make(map[sessionKey]models.Session)

	for i, gene := range c {
		item := &s.schedulableItems[i]
		key := sessionKey{item.LessonID, item.DistIdx}

		roomID := ""
		if gene.RoomIdx != -1 {
			roomID = s.rooms[gene.RoomIdx].ID
		}

		if sess, ok := merged[key]; ok {
			// Aggregate rooms
			if roomID != "" && !strings.Contains(sess.Room, roomID) {
				if sess.Room == "" {
					sess.Room = roomID
				} else {
					sess.Room += ", " + roomID
				}
			}

			// Aggregate Affected Entities
			sess.AffectedGroups = mergeUnique(sess.AffectedGroups, item.AffectedGrps)
			if item.Instructor != "" {
				sess.AffectedInstructors = mergeUnique(sess.AffectedInstructors, []string{item.Instructor})
			}

			// Add to MultipleIDs to preserve mapping
			if item.LessonDef.Multiple {
				sess.MultipleIDs = append(sess.MultipleIDs, models.SubgroupID{
					GroupID:        item.Group,
					InstID:         item.Instructor,
					UnitID:         item.Unit,
					RoomID:         roomID,
					AffectedGroups: item.AffectedGrps,
				})
			}

			merged[key] = sess
		} else {
			dbIDs := item.LessonDef.DatabaseIDs
			if !item.Online && gene.RoomIdx != -1 {
				dbIDs.Room = s.rooms[gene.RoomIdx].DatabaseID
			}

			affectedGrps := make([]string, len(item.AffectedGrps))
			copy(affectedGrps, item.AffectedGrps)

			affectedInsts := []string{}
			if item.Instructor != "" {
				affectedInsts = []string{item.Instructor}
			}

			newSess := models.Session{
				Identifier:          fmt.Sprintf("%s-%d", item.LessonID, item.DistIdx),
				Group:               item.Group,
				Instructor:          item.Instructor,
				Unit:                item.Unit,
				Day:                 s.days[gene.DayIdx],
				Time:                s.blocks[gene.BlockIdx].Start,
				Room:                roomID,
				Online:              item.Online,
				Blocks:              item.Length,
				DatabaseIDs:         dbIDs,
				TimetableID:         item.LessonDef.TimetableID,
				AffectedGroups:      affectedGrps,
				AffectedInstructors: affectedInsts,
				Multiple:            item.LessonDef.Multiple,
				Type:                item.LessonDef.Type,
				Short:               item.LessonDef.Short,
				Title:               item.LessonDef.Title,
			}

			// Populate Times array for multi-block support
			for b := 0; b < item.Length; b++ {
				blockIdx := gene.BlockIdx + b
				if blockIdx < len(s.blocks) {
					newSess.Times = append(newSess.Times, s.blocks[blockIdx].Start)
				}
			}

			if item.LessonDef.Multiple {
				if item.LessonDef.Type == "lesson-merge" {
					// Expand all merged combinations immediately
					for _, mid := range item.LessonDef.MultipleIDs {
						newSess.MultipleIDs = append(newSess.MultipleIDs, models.SubgroupID{
							GroupID:        mid.GroupID,
							GroupDatabase:  mid.GroupDatabase,
							InstID:         item.Instructor,
							InstDatabase:   item.LessonDef.DatabaseIDs.Instructor,
							UnitID:         item.Unit,
							UnitDatabase:   item.LessonDef.DatabaseIDs.Unit,
							RoomID:         roomID,
							RoomDatabase:   dbIDs.Room,
							Online:         item.Online,
							AffectedGroups: []string{mid.GroupID},
						})
					}
				} else {
					// Initial combination for a subgroup
					mid := item.LessonDef.MultipleIDs[item.MidIdx]
					newSess.MultipleIDs = []models.SubgroupID{{
						GroupID:        mid.GroupID,
						GroupDatabase:  mid.GroupDatabase,
						InstID:         mid.InstID,
						InstDatabase:   mid.InstDatabase,
						UnitID:         mid.UnitID,
						UnitDatabase:   mid.UnitDatabase,
						RoomID:         roomID,
						RoomDatabase:   dbIDs.Room,
						Online:         item.Online,
						AffectedGroups: mid.AffectedGroups,
					}}
				}
			}

			merged[key] = newSess
		}
	}

	sessions := make([]models.Session, 0, len(merged))
	for _, sess := range merged {
		sessions = append(sessions, sess)
	}
	return sessions
}

func mergeUnique(a, b []string) []string {
	m := make(map[string]bool)
	for _, x := range a {
		if x != "" {
			m[x] = true
		}
	}
	for _, x := range b {
		if x != "" {
			m[x] = true
		}
	}
	res := make([]string, 0, len(m))
	for x := range m {
		res = append(res, x)
	}
	sort.Strings(res)
	return res
}

// ---------------------------------------------------------------------------
// Evolution (Phase 2)
// ---------------------------------------------------------------------------

func (s *GeneticSolver) evolve(population []Chromosome, scores []float64) []Chromosome {
	newPop := make([]Chromosome, 0, s.PopulationSize)

	// Elitism — copy the top EliteSize chromosomes unchanged.
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

	// Reproduction.
	for len(newPop) < s.PopulationSize {
		p1 := s.tournamentSelect(population, scores)
		p2 := s.tournamentSelect(population, scores)

		var c1, c2 Chromosome
		if rand.Float64() < s.CrossoverRate {
			c1, c2 = s.twoPointCrossover(p1, p2)
		} else {
			c1 = make(Chromosome, len(p1))
			c2 = make(Chromosome, len(p2))
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
	best := -1
	for i := 0; i < 5; i++ {
		idx := rand.Intn(len(pop))
		if best == -1 || scores[idx] > scores[best] {
			best = idx
		}
	}
	result := make(Chromosome, len(pop[best]))
	copy(result, pop[best])
	return result
}

// twoPointCrossover swaps the segment [pt1, pt2) between the two parents.
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

// mutate applies two passes:
//  1. Targeted repair — any gene still involved in a clash is re-placed
//     greedily (same logic as repairChromosome).
//  2. Random noise — 5% of non-clashing genes get a random new slot to
//     prevent premature convergence.
func (s *GeneticSolver) mutate(c Chromosome) {
	// Pass 1: repair clashing genes.
	clashing := s.findClashingGenes(c)
	if len(clashing) > 0 {
		occ := newOccupancyMaps()
		for i, gene := range c {
			if !clashing[i] {
				occ.add(&s.schedulableItems[i], gene)
			}
		}
		for idx := range clashing {
			item := &s.schedulableItems[idx]
			newGene := s.bestPlacement(item, occ, 20)
			c[idx] = newGene
			occ.add(item, newGene)
		}
	}

	// Pass 2: random noise on non-clashing genes.
	for i := range c {
		if clashing[i] {
			continue
		}
		if rand.Float64() < s.MutationRate {
			item := &s.schedulableItems[i]
			maxBlock := len(s.blocks) - item.Length
			if maxBlock < 0 {
				maxBlock = 0
			}
			c[i].DayIdx = rand.Intn(len(s.days))
			if maxBlock > 0 {
				c[i].BlockIdx = rand.Intn(maxBlock + 1)
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
		Error:   false,
		Message: "Optimization successful",
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
	for _, inst := range req.Instructors {
		s.instructorMap[inst.ID] = inst
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
			switch l.Type {
			case "regular":
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

			case "lesson-merge":
				affected := make([]string, 0, len(l.MultipleIDs))
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

			case "subgroup", "subgroup-lesson-merge":
				for midIdx, m := range l.MultipleIDs {
					affected := m.AffectedGroups
					if len(affected) == 0 && m.GroupID != "" {
						affected = []string{m.GroupID}
					}
					s.schedulableItems = append(s.schedulableItems, SchedulableItem{
						LessonID:     l.Identifier,
						DistIdx:      distIdx,
						MidIdx:       midIdx,
						Length:       count,
						Instructor:   m.InstID,
						Group:        m.GroupID,
						Unit:         m.UnitID,
						Online:       l.Online,
						AffectedGrps: affected,
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
	for _, inst := range s.instructorMap {
		instructors = append(instructors, inst)
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

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (s *GeneticSolver) IsSubgroup(t string) bool {
	return t == "subgroup" || t == "subgroup-lesson-merge"
}

// argmax returns the index of the highest value in a slice.
func argmax(scores []float64) int {
	best := 0
	for i, v := range scores {
		if v > scores[best] {
			best = i
		}
	}
	return best
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

// Ensure math import is used.
var _ = math.MaxInt32