package solver

import (
	"context"
	"fmt"
	"geliana-go/pkg/models"
	"math"
	"math/rand"
	"time"
)

// SimulatedAnnealingSolver implements Simulated Annealing for timetable optimization.
type SimulatedAnnealingSolver struct {
	InitialTemp float64
	CoolingRate float64
	MinTemp     float64
	MaxIter     int
	
	schedulableItems []SchedulableItem
	rooms            []models.Room
	days             []string
	blocks           []models.Block
	
	roomMap       map[string]int
	unitMap       map[string]models.Unit
	instructorMap map[string]models.Instructor
	groupMap      map[string]models.Group
}

func NewSimulatedAnnealingSolver(iter int) *SimulatedAnnealingSolver {
	return &SimulatedAnnealingSolver{
		InitialTemp: 1000.0,
		CoolingRate: 0.995,
		MinTemp:     0.1,
		MaxIter:     iter,
	}
}

func (s *SimulatedAnnealingSolver) Solve(ctx context.Context, req *models.Request, tracker ProgressTracker) (*models.Response, error) {
	startTime := time.Now()
	
	// 1. Preprocessing (reuse logic or refactor to shared helper)
	s.preprocess(req)
	if len(s.schedulableItems) == 0 {
		return nil, fmt.Errorf("no schedulable items found")
	}

	// 2. Initial Solution
	currentSolution := s.initializeSolution()
	currentCost := s.calculateCost(currentSolution)
	
	bestSolution := make(Chromosome, len(currentSolution))
	copy(bestSolution, currentSolution)
	bestCost := currentCost

	temp := s.InitialTemp

	// 3. Main Loop
	for i := 0; i < s.MaxIter && temp > s.MinTemp; i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		neighbor := s.getNeighbor(currentSolution)
		neighborCost := s.calculateCost(neighbor)
		
		delta := neighborCost - currentCost
		if delta < 0 || rand.Float64() < math.Exp(-delta/temp) {
			currentSolution = neighbor
			currentCost = neighborCost
			
			if currentCost < bestCost {
				bestCost = currentCost
				copy(bestSolution, currentSolution)
				if bestCost == 0 {
					break
				}
			}
		}

		temp *= s.CoolingRate

		// Progress Report (throttled)
		if tracker != nil && i%100 == 0 {
			tracker.Update(ProgressReport{
				CurrentStep: i + 1,
				TotalSteps:  s.MaxIter,
				StepLabel:   fmt.Sprintf("Iteration %d", i),
				BestFitness: -bestCost,
				Metrics: map[string]interface{}{
					"best_cost":   bestCost,
					"temp":        temp,
					"iteration":   i,
				},
				Trace: &models.TraceStep{
					Type:  "iteration",
					Label: fmt.Sprintf("Iteration %d", i),
					Score: -bestCost,
				},
			})
		}
	}

	runtime := time.Since(startTime).Seconds()
	if bestCost > 0 && ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// 4. Output Generation
	return s.formatResponse(bestSolution, runtime, bestCost), nil
}

// Preprocess logic (Ported from GA, should ideally be shared)
func (s *SimulatedAnnealingSolver) preprocess(req *models.Request) {
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
			
			// Same flattening logic as GA
			if l.Type == "regular" {
				s.schedulableItems = append(s.schedulableItems, SchedulableItem{
					LessonID: l.Identifier, DistIdx: distIdx, Length: count,
					Instructor: l.Instructor, Group: l.Group, Unit: l.Unit,
					Online: l.Online, AffectedGrps: []string{l.Group}, LessonDef: &l,
				})
			} else if l.Type == "lesson-merge" {
				var affected []string
				for _, m := range l.MultipleIDs {
					affected = append(affected, m.GroupID)
				}
				s.schedulableItems = append(s.schedulableItems, SchedulableItem{
					LessonID: l.Identifier, DistIdx: distIdx, Length: count,
					Instructor: l.Instructor, Group: "", Unit: l.Unit,
					Online: l.Online, AffectedGrps: affected, LessonDef: &l,
				})
			} else if l.Type == "subgroup" || l.Type == "subgroup-lesson-merge" {
				for midIdx, m := range l.MultipleIDs {
					s.schedulableItems = append(s.schedulableItems, SchedulableItem{
						LessonID: l.Identifier, DistIdx: distIdx, MidIdx: midIdx, Length: count,
						Instructor: m.InstID, Group: m.GroupID, Unit: m.UnitID,
						Online: l.Online, AffectedGrps: m.AffectedGroups, LessonDef: &l,
					})
				}
			}
		}
	}
}

func (s *SimulatedAnnealingSolver) initializeSolution() Chromosome {
	sol := make(Chromosome, len(s.schedulableItems))
	for i, item := range s.schedulableItems {
		dayIdx := rand.Intn(len(s.days))
		maxStart := len(s.blocks) - item.Length
		blockIdx := 0
		if maxStart > 0 {
			blockIdx = rand.Intn(maxStart + 1)
		}
		roomIdx := -1
		if !item.Online && len(s.rooms) > 0 {
			roomIdx = rand.Intn(len(s.rooms))
		}
		sol[i] = Gene{DayIdx: dayIdx, BlockIdx: blockIdx, RoomIdx: roomIdx}
	}
	return sol
}

func (s *SimulatedAnnealingSolver) calculateCost(sol Chromosome) float64 {
	// Reusing GA fitness logic but returning positive penalty
	penalty := 0.0
	
	type slot struct{ day, start, end int }
	instSchedules := make(map[string][]slot)
	roomSchedules := make(map[int][]slot)
	groupSchedules := make(map[string][]slot)
	syncMap := make(map[string]slot)

	for i, gene := range sol {
		item := s.schedulableItems[i]
		endBlock := gene.BlockIdx + item.Length
		
		syncKey := fmt.Sprintf("%s_%d", item.LessonID, item.DistIdx)
		if item.LessonDef.Type == "subgroup" || item.LessonDef.Type == "subgroup-lesson-merge" {
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
			if grp == "" { continue }
			for _, sc := range groupSchedules[grp] {
				if sc.day == gene.DayIdx && max(gene.BlockIdx, sc.start) < min(endBlock, sc.end) {
					penalty += 1000
				}
			}
			groupSchedules[grp] = append(groupSchedules[grp], slot{gene.DayIdx, gene.BlockIdx, endBlock})
		}
	}
	return penalty
}

func (s *SimulatedAnnealingSolver) getNeighbor(current Chromosome) Chromosome {
	neighbor := make(Chromosome, len(current))
	copy(neighbor, current)
	
	idx := rand.Intn(len(neighbor))
	item := s.schedulableItems[idx]
	
	moveType := rand.Float64()
	if moveType < 0.6 {
		// Time/Day change
		neighbor[idx].DayIdx = rand.Intn(len(s.days))
		maxStart := len(s.blocks) - item.Length
		if maxStart > 0 {
			neighbor[idx].BlockIdx = rand.Intn(maxStart + 1)
		} else {
			neighbor[idx].BlockIdx = 0
		}
	} else if !item.Online && len(s.rooms) > 0 {
		// Room change
		neighbor[idx].RoomIdx = rand.Intn(len(s.rooms))
	}
	
	return neighbor
}

func (s *SimulatedAnnealingSolver) formatResponse(c Chromosome, runtime, cost float64) *models.Response {
	// Reusing GA response formatting (should be refactored)
	// (Omitted for brevity, but logically identical to GA's formatResponse)
	// Since I cannot call GA's private method, I'll copy the logic here.
	
	var sessions []models.Session
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
		Message:  fmt.Sprintf("Optimization completed with cost %.2f", cost),
		Sessions: sessions,
		Stats: models.OptimizationStats{
			OverallScore:  math.Max(0, 100-(cost/10.0)), // Dummy score mapping
			TimeTaken:     runtime,
			SolutionFound: cost < 1000, // Considered found if no hard clashes
		},
	}
}
