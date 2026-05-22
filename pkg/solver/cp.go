package solver

import (
	"context"
	"fmt"
	"geliana-go/pkg/models"
	"time"
)

// Slot represents a potential assignment: (Day, Block, Room)
type Slot struct {
	DayIdx   int
	BlockIdx int
	RoomIdx  int // -1 for online
}

// CPSolver implements a pure Go Constraint Satisfaction solver.
type CPSolver struct {
	schedulableItems []SchedulableItem
	rooms            []models.Room
	days             []string
	blocks           []models.Block
	
	// Initial domains for each variable (schedulable item)
	initialDomains [][]Slot
	
	roomMap       map[string]int
	unitMap       map[string]models.Unit
	instructorMap map[string]models.Instructor
	groupMap      map[string]models.Group
}

func NewCPSolver() *CPSolver {
	return &CPSolver{}
}

func (s *CPSolver) Solve(ctx context.Context, req *models.Request, tracker ProgressTracker) (*models.Response, error) {
	startTime := time.Now()
	
	// 1. Preprocessing (reuse logic)
	s.preprocess(req)
	if len(s.schedulableItems) == 0 {
		return nil, fmt.Errorf("no schedulable items found")
	}

	// 2. Domain Initialization & Pruning
	s.initializeDomains()
	if err := s.pruneDomains(); err != nil {
		return nil, err
	}

	// 3. Backtracking Search
	assignment := make(Chromosome, len(s.schedulableItems))
	for i := range assignment {
		assignment[i] = Gene{DayIdx: -1, BlockIdx: -1, RoomIdx: -2} // Unassigned
	}

	found, result := s.backtrack(ctx, assignment, 0, tracker)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	if !found {
		return &models.Response{
			Error:   true,
			Message: "Infeasible: No valid clash-free solution exists",
			Stats: models.OptimizationStats{
				SolutionFound: false,
				FailReason:    "INFEASIBLE",
				TimeTaken:     time.Since(startTime).Seconds(),
			},
		}, nil
	}

	return s.formatResponse(result, time.Since(startTime).Seconds()), nil
}

// preprocess reuses the logic from GeneticSolver (should eventually be shared)
func (s *CPSolver) preprocess(req *models.Request) {
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
			if count == 0 { continue }
			if l.Type == "regular" {
				s.schedulableItems = append(s.schedulableItems, SchedulableItem{
					LessonID: l.Identifier, DistIdx: distIdx, Length: count,
					Instructor: l.Instructor, Group: l.Group, Unit: l.Unit,
					Online: l.Online, AffectedGrps: []string{l.Group}, LessonDef: &l,
				})
			} else if l.Type == "lesson-merge" {
				var affected []string
				for _, m := range l.MultipleIDs { affected = append(affected, m.GroupID) }
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

func (s *CPSolver) initializeDomains() {
	s.initialDomains = make([][]Slot, len(s.schedulableItems))
	for i, item := range s.schedulableItems {
		var domain []Slot
		maxStart := len(s.blocks) - item.Length
		for d := 0; d < len(s.days); d++ {
			for b := 0; b <= maxStart; b++ {
				if item.Online {
					domain = append(domain, Slot{DayIdx: d, BlockIdx: b, RoomIdx: -1})
				} else {
					for r := 0; r < len(s.rooms); r++ {
						domain = append(domain, Slot{DayIdx: d, BlockIdx: b, RoomIdx: r})
					}
				}
			}
		}
		s.initialDomains[i] = domain
	}
}

// pruneDomains applies fixed preferences and hard constraints before searching.
func (s *CPSolver) pruneDomains() error {
	for i, item := range s.schedulableItems {
		var pruned []Slot
		for _, slot := range s.initialDomains[i] {
			if s.isSlotAllowed(item, slot) {
				pruned = append(pruned, slot)
			}
		}
		if len(pruned) == 0 {
			return fmt.Errorf("infeasible: lesson %s has no valid slots after pruning preferences", item.LessonID)
		}
		s.initialDomains[i] = pruned
	}
	return nil
}

func (s *CPSolver) isSlotAllowed(item SchedulableItem, slot Slot) bool {
	// 1. Room Capacity
	if slot.RoomIdx != -1 {
		room := s.rooms[slot.RoomIdx]
		for _, grpID := range item.AffectedGrps {
			if grp, ok := s.groupMap[grpID]; ok && grp.Total > room.Capacity {
				return false
			}
		}
	}

	// 2. Preferences (ONLY, EXCEPT, BEFORE, AFTER)
	// (Implementation simplified for now: matches Day/Time strings)
	dayName := s.days[slot.DayIdx]
	startTime := s.blocks[slot.BlockIdx].Start
	
	// Check Instructor Preferences
	if inst, ok := s.instructorMap[item.Instructor]; ok {
		if !s.checkPreferences(inst.Preferences, dayName, startTime) { return false }
	}
	// Check Group Preferences
	for _, grpID := range item.AffectedGrps {
		if grp, ok := s.groupMap[grpID]; ok {
			if !s.checkPreferences(grp.Preferences, dayName, startTime) { return false }
		}
	}
	// Check Unit Preferences
	if unit, ok := s.unitMap[item.Unit]; ok {
		if !s.checkPreferences(unit.Preferences, dayName, startTime) { return false }
	}

	return true
}

func (s *CPSolver) checkPreferences(prefs []models.Preference, day, start string) bool {
	for _, p := range prefs {
		// Example: UNAVAILABLE_TIMES or EXCEPT
		if p.Type == "unavailable_times" || p.Type == "EXCEPT" {
			for _, d := range p.Days {
				if d == day {
					if len(p.Times) == 0 { return false } // All day unavailable
					for _, t := range p.Times {
						if t == start { return false }
					}
				}
			}
		}
		// Add more preference type checks here (ONLY, BEFORE, AFTER)
	}
	return true
}

func (s *CPSolver) backtrack(ctx context.Context, assignment Chromosome, step int, tracker ProgressTracker) (bool, Chromosome) {
	select {
	case <-ctx.Done():
		return false, nil
	default:
	}

	if step == len(s.schedulableItems) {
		return true, assignment
	}

	// Heuristic: Pick variable with MRV (Minimum Remaining Values)
	varIdx := s.pickMRVVariable(assignment)
	
	// Try each slot in domain
	for _, slot := range s.initialDomains[varIdx] {
		if s.isConsistent(varIdx, slot, assignment) {
			assignment[varIdx] = Gene{DayIdx: slot.DayIdx, BlockIdx: slot.BlockIdx, RoomIdx: slot.RoomIdx}
			
			if tracker != nil {
				tracker.Update(ProgressReport{
					Trace: &models.TraceStep{
						Type:  "assignment",
						Label: fmt.Sprintf("Assign %s to (D%d, B%d, R%d)", s.schedulableItems[varIdx].LessonID, slot.DayIdx, slot.BlockIdx, slot.RoomIdx),
						Score: 0,
					},
				})
			}

			if found, res := s.backtrack(ctx, assignment, step+1, tracker); found {
				return true, res
			}
			
			if tracker != nil {
				tracker.Update(ProgressReport{
					Trace: &models.TraceStep{
						Type:  "backtrack",
						Label: fmt.Sprintf("Backtrack from %s", s.schedulableItems[varIdx].LessonID),
						Score: 0,
					},
				})
			}

			// Backtrack
			assignment[varIdx] = Gene{DayIdx: -1, BlockIdx: -1, RoomIdx: -2}
		}
	}

	return false, nil
}

func (s *CPSolver) pickMRVVariable(assignment Chromosome) int {
	bestIdx := -1
	minDomainSize := 1e9

	for i, gene := range assignment {
		if gene.DayIdx != -1 {
			continue // Already assigned
		}

		// Count currently valid slots in domain
		// (In full forward checking, this would use pruned domains)
		// For now, we count slots that are consistent with current partial assignment
		validCount := 0
		for _, slot := range s.initialDomains[i] {
			if s.isConsistent(i, slot, assignment) {
				validCount++
			}
		}

		if float64(validCount) < minDomainSize {
			minDomainSize = float64(validCount)
			bestIdx = i
		}
	}
	return bestIdx
}

func (s *CPSolver) isConsistent(varIdx int, slot Slot, assignment Chromosome) bool {
	item := s.schedulableItems[varIdx]
	end := slot.BlockIdx + item.Length

	for i, gene := range assignment {
		if i == varIdx || gene.DayIdx == -1 { continue }
		
		other := s.schedulableItems[i]
		otherEnd := gene.BlockIdx + other.Length
		
		// Only check if on the same day
		if gene.DayIdx != slot.DayIdx { continue }
		
		// Check overlap
		overlaps := max(slot.BlockIdx, gene.BlockIdx) < min(end, otherEnd)
		if !overlaps { continue }

		// 1. Instructor Clash
		if item.Instructor == other.Instructor { return false }

		// 2. Room Clash
		if slot.RoomIdx != -1 && slot.RoomIdx == gene.RoomIdx { return false }

		// 3. Group Clash
		for _, g1 := range item.AffectedGrps {
			if g1 == "" { continue }
			for _, g2 := range other.AffectedGrps {
				if g1 == g2 { return false }
			}
		}
		
		// 4. Sync constraint (subgroups must be at same time)
		if item.LessonID == other.LessonID && item.DistIdx == other.DistIdx {
			// If they are in the same lesson group, they MUST be at the same time
			if slot.DayIdx != gene.DayIdx || slot.BlockIdx != gene.BlockIdx {
				return false
			}
		}
	}
	return true
}

func (s *CPSolver) formatResponse(c Chromosome, runtime float64) *models.Response {
	// Use same formatting as GeneticSolver
	gs := &GeneticSolver{rooms: s.rooms, days: s.days, blocks: s.blocks, schedulableItems: s.schedulableItems, groupMap: s.groupMap}
	return gs.formatResponse(c, runtime)
}
