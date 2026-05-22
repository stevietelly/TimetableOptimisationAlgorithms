package solver

import (
	"context"
	"fmt"
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

// GeneticSolver implements a Genetic Algorithm for timetable optimization.
type GeneticSolver struct {
	PopulationSize int
	EliteSize      int
	MutationRate   float64
	CrossoverRate  float64
	MaxGenerations int
	
	schedulableItems []SchedulableItem
	rooms            []models.Room
	days             []string
	blocks           []models.Block
	
	roomMap       map[string]int
	unitMap       map[string]models.Unit
	instructorMap map[string]models.Instructor
	groupMap      map[string]models.Group
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
		MutationRate:   0.1,
		CrossoverRate:  0.8,
		MaxGenerations: maxGen,
	}
}

func (s *GeneticSolver) Solve(ctx context.Context, req *models.Request, tracker ProgressTracker) (*models.Response, error) {
	startTime := time.Now()
	
	// 1. Preprocessing
	s.preprocess(req)
	if len(s.schedulableItems) == 0 {
		return nil, fmt.Errorf("no schedulable items found")
	}

	// 2. Initial Population
	population := s.initializePopulation(ctx)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	var bestChromosome Chromosome
	bestFitness := -1e18

	// 3. Main Loop
	for gen := 0; gen < s.MaxGenerations; gen++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		// Parallel Fitness Evaluation
		fitnessScores := s.evaluatePopulation(population)
		
		// Track Best
		currentBestIdx := 0
		for i, f := range fitnessScores {
			if f > bestFitness {
				bestFitness = f
				bestChromosome = make(Chromosome, len(population[i]))
				copy(bestChromosome, population[i])
			}
			if f > fitnessScores[currentBestIdx] {
				currentBestIdx = i
			}
		}

		// Progress Report
		if tracker != nil {
			tracker.Update(ProgressReport{
				CurrentStep: gen + 1,
				TotalSteps:  s.MaxGenerations,
				StepLabel:   fmt.Sprintf("Generation %d", gen),
				BestFitness: bestFitness,
				Metrics: map[string]interface{}{
					"best_fitness": bestFitness,
					"gen":          gen,
				},
				Trace: &models.TraceStep{
					Type:  "generation",
					Label: fmt.Sprintf("Generation %d", gen),
					Score: bestFitness,
				},
			})
		}

		// Early exit if optimal
		if bestFitness == 0 {
			break
		}

		// Selection & Reproduction
		population = s.evolve(population, fitnessScores)
	}

	if bestChromosome == nil {
		return nil, fmt.Errorf("no solution found")
	}

	// 4. Output Generation
	return s.formatResponse(bestChromosome, time.Since(startTime).Seconds()), nil
}

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
		l := lesson // Local copy
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

func (s *GeneticSolver) initializePopulation(ctx context.Context) []Chromosome {
	population := make([]Chromosome, s.PopulationSize)
	for i := 0; i < s.PopulationSize; i++ {
		chromosome := make(Chromosome, len(s.schedulableItems))
		for j, item := range s.schedulableItems {
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
			chromosome[j] = Gene{DayIdx: dayIdx, BlockIdx: blockIdx, RoomIdx: roomIdx}
		}
		population[i] = chromosome
	}
	return population
}

func (s *GeneticSolver) evaluatePopulation(population []Chromosome) []float64 {
	scores := make([]float64, len(population))
	var wg sync.WaitGroup
	wg.Add(len(population))
	
	for i := range population {
		go func(idx int) {
			defer wg.Done()
			scores[idx] = s.calculateFitness(population[idx])
		}(i)
	}
	
	wg.Wait()
	return scores
}

func (s *GeneticSolver) calculateFitness(chromosome Chromosome) float64 {
	penalty := 0.0
	
	type slot struct {
		day, start, end int
	}
	instSchedules := make(map[string][]slot)
	roomSchedules := make(map[int][]slot)
	groupSchedules := make(map[string][]slot)
	syncMap := make(map[string]slot) // (LessonID, DistIdx) -> slot

	for i, gene := range chromosome {
		item := s.schedulableItems[i]
		endBlock := gene.BlockIdx + item.Length
		
		// 1. Sync Penalty (subgroups must be at the same time)
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

		// 2. Instructor Conflict
		for _, s := range instSchedules[item.Instructor] {
			if s.day == gene.DayIdx {
				if max(gene.BlockIdx, s.start) < min(endBlock, s.end) {
					penalty += 1000
				}
			}
		}
		instSchedules[item.Instructor] = append(instSchedules[item.Instructor], slot{gene.DayIdx, gene.BlockIdx, endBlock})

		// 3. Room Conflict
		if !item.Online && gene.RoomIdx != -1 {
			for _, s := range roomSchedules[gene.RoomIdx] {
				if s.day == gene.DayIdx {
					if max(gene.BlockIdx, s.start) < min(endBlock, s.end) {
						penalty += 1000
					}
				}
			}
			roomSchedules[gene.RoomIdx] = append(roomSchedules[gene.RoomIdx], slot{gene.DayIdx, gene.BlockIdx, endBlock})
		}

		// 4. Group Conflict
		for _, grp := range item.AffectedGrps {
			if grp == "" {
				continue
			}
			for _, s := range groupSchedules[grp] {
				if s.day == gene.DayIdx {
					if max(gene.BlockIdx, s.start) < min(endBlock, s.end) {
						penalty += 1000
					}
				}
			}
			groupSchedules[grp] = append(groupSchedules[grp], slot{gene.DayIdx, gene.BlockIdx, endBlock})
		}
		
		// 5. Room Capacity
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

	return -penalty
}

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
			c1, c2 = s.crossover(p1, p2)
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
	k := 3
	best := -1
	for i := 0; i < k; i++ {
		idx := rand.Intn(len(pop))
		if best == -1 || scores[idx] > scores[best] {
			best = idx
		}
	}
	return pop[best]
}

func (s *GeneticSolver) crossover(p1, p2 Chromosome) (Chromosome, Chromosome) {
	c1, c2 := make(Chromosome, len(p1)), make(Chromosome, len(p2))
	for i := range p1 {
		if rand.Float64() < 0.5 {
			c1[i], c2[i] = p1[i], p2[i]
		} else {
			c1[i], c2[i] = p2[i], p1[i]
		}
	}
	return c1, c2
}

func (s *GeneticSolver) mutate(c Chromosome) {
	for i := range c {
		if rand.Float64() < s.MutationRate {
			item := s.schedulableItems[i]
			c[i].DayIdx = rand.Intn(len(s.days))
			maxStart := len(s.blocks) - item.Length
			if maxStart < 0 {
				maxStart = 0
			}
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

func (s *GeneticSolver) formatResponse(c Chromosome, runtime float64) *models.Response {
	var sessions []models.Session
	
	// Subgroup handling: group by (LessonID, DistIdx) to collect multiple rooms/IDs
	// For simplicity in this basic version, we'll just create a session for each schedulable item
	// and merge them if they share the same LessonID and DistIdx.
	
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
			// Update merged session (e.g., append room if different)
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
				DatabaseIDs: dbIDs,
				TimetableID: item.LessonDef.TimetableID,
			}
		}
	}

	for _, sess := range merged {
		sessions = append(sessions, sess)
	}

	return &models.Response{
		Error:    false,
		Message:  "Optimization successful",
		Sessions: sessions,
		Stats: models.OptimizationStats{
			OverallScore:  100.0, // TODO: Calculate actual score
			TimeTaken:     runtime,
			SolutionFound: true,
		},
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
