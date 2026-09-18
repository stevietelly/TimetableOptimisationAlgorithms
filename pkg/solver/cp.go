package solver

import (
	"context"
	"fmt"
	"geliana-go/pkg/models"
	"math/rand"
	"sort"
	"time"
)

// Slot represents a potential assignment: (Day, Block, Room)
type Slot struct {
	DayIdx   int
	BlockIdx int
	RoomIdx  int // -1 for online
}

// CPSolver implements a Constraint Satisfaction solver with:
//   - Forward checking (domain pruning after each assignment)
//   - MRV (Minimum Remaining Values) variable ordering
//   - Degree heuristic as MRV tie-breaker
//   - Conflict-directed backjumping (bounded)
//   - Hard cap on total backtracks — falls through to GA repair if hit
type CPSolver struct {
	schedulableItems []SchedulableItem
	rooms            []models.Room
	days             []string
	blocks           []models.Block

	// domains[i] is the current working domain for item i.
	// It is pruned as assignments are made and restored on backtrack.
	domains [][]Slot

	// initialDomains[i] is the preference-pruned but conflict-free starting
	// domain. Never mutated after pruneDomains().
	initialDomains [][]Slot

	// assignmentOrder tracks which variable index was assigned at each depth,
	// so we can unwind correctly on backtrack.
	assignmentOrder []int

	// constraintGraph[i] = list of item indices that share a constraint with i
	// (same instructor, overlapping groups, etc.). Used for degree heuristic.
	constraintGraph [][]int

	roomMap       map[string]int
	unitMap       map[string]models.Unit
	instructorMap map[string]models.Instructor
	groupMap      map[string]models.Group

	backtrackCount int
	maxBacktracks  int // hard cap — solver gives up and returns best partial
}

func NewCPSolver() *CPSolver {
	return &CPSolver{
		maxBacktracks: 500,
	}
}

// ---------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------

func (s *CPSolver) Solve(ctx context.Context, req *models.Request, tracker ProgressTracker) (*models.Response, error) {
	startTime := time.Now()

	s.preprocess(req)
	if len(s.schedulableItems) == 0 {
		return nil, fmt.Errorf("no schedulable items found")
	}

	s.initializeDomains()
	if err := s.pruneDomains(); err != nil {
		return nil, err
	}
	s.buildConstraintGraph()

	s.backtrackCount = 0
	s.assignmentOrder = make([]int, 0, len(s.schedulableItems))

	// assignment[i] = Gene assigned to schedulableItems[i].
	// DayIdx == -1 means unassigned.
	assignment := make(Chromosome, len(s.schedulableItems))
	for i := range assignment {
		assignment[i] = Gene{DayIdx: -1, BlockIdx: -1, RoomIdx: -2}
	}

	// Deep-copy domains so forward checking can mutate them freely.
	s.domains = deepCopyDomains(s.initialDomains)

	found, result := s.backtrack(ctx, assignment, 0, tracker)

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	if !found {
		// If we hit the backtrack cap we return the best partial assignment
		// rather than failing — the GA will repair the remaining clashes.
		if s.backtrackCount >= s.maxBacktracks {
			return s.formatResponse(s.bestPartial(assignment), time.Since(startTime).Seconds()), nil
		}
		return &models.Response{
			Error:   true,
			Message: "Infeasible: no valid clash-free solution found",
			Stats: models.OptimizationStats{
				SolutionFound: false,
				FailReason:    "INFEASIBLE",
				TimeTaken:     time.Since(startTime).Seconds(),
			},
		}, nil
	}

	return s.formatResponse(result, time.Since(startTime).Seconds()), nil
}

// ---------------------------------------------------------------------------
// Core backtracking search
// ---------------------------------------------------------------------------

func (s *CPSolver) backtrack(ctx context.Context, assignment Chromosome, depth int, tracker ProgressTracker) (bool, Chromosome) {
	select {
	case <-ctx.Done():
		return false, nil
	default:
	}

	// All items assigned — solution found.
	if depth == len(s.schedulableItems) {
		return true, assignment
	}

	// Hard cap: stop burning time and let the GA clean up.
	if s.backtrackCount >= s.maxBacktracks {
		return false, nil
	}

	// Choose the next variable using MRV + degree heuristic.
	varIdx := s.pickVariable(assignment)
	if varIdx == -1 {
		// All unassigned variables have empty domains — dead end.
		return false, nil
	}

	// Work on a copy of this variable's domain so we can iterate safely
	// while forward checking mutates s.domains.
	domainSnapshot := make([]Slot, len(s.domains[varIdx]))
	copy(domainSnapshot, s.domains[varIdx])

	// Shuffle to avoid always trying slots in the same order, which causes
	// the solver to converge on the same dead end repeatedly.
	rand.Shuffle(len(domainSnapshot), func(i, j int) {
		domainSnapshot[i], domainSnapshot[j] = domainSnapshot[j], domainSnapshot[i]
	})

	for _, slot := range domainSnapshot {
		if !s.isConsistent(varIdx, slot, assignment) {
			continue
		}

		// Make the assignment.
		assignment[varIdx] = Gene{DayIdx: slot.DayIdx, BlockIdx: slot.BlockIdx, RoomIdx: slot.RoomIdx}
		s.assignmentOrder = append(s.assignmentOrder, varIdx)

		// Forward checking: prune domains of unassigned neighbours.
		// Save the pruned slots so we can restore them on backtrack.
		pruned := s.forwardCheck(varIdx, slot, assignment)
		domainWipedOut := false
		for _, p := range pruned {
			if len(s.domains[p.itemIdx]) == 0 {
				domainWipedOut = true
				break
			}
		}

		if tracker != nil {
			tracker.Update(ProgressReport{
				CurrentStep: depth + 1,
				TotalSteps:  len(s.schedulableItems),
				StepLabel:   fmt.Sprintf("Assigning %d/%d", depth+1, len(s.schedulableItems)),
				Metrics: map[string]interface{}{
					"assigned_count":  depth + 1,
					"total_count":     len(s.schedulableItems),
					"backtrack_count": s.backtrackCount,
				},
				Trace: &models.TraceStep{
					Type:  "assignment",
					Label: fmt.Sprintf("Assign item %d (D%d,B%d,R%d)", varIdx, slot.DayIdx, slot.BlockIdx, slot.RoomIdx),
					Score: 0,
				},
			})
		}

		if !domainWipedOut {
			if found, res := s.backtrack(ctx, assignment, depth+1, tracker); found {
				return true, res
			}
		}

		// Undo: restore pruned domains and unassign.
		s.restorePruned(pruned)
		assignment[varIdx] = Gene{DayIdx: -1, BlockIdx: -1, RoomIdx: -2}
		s.assignmentOrder = s.assignmentOrder[:len(s.assignmentOrder)-1]

		s.backtrackCount++

		if tracker != nil {
			tracker.Update(ProgressReport{
				CurrentStep: depth,
				TotalSteps:  len(s.schedulableItems),
				StepLabel:   fmt.Sprintf("Backtracking %d/%d (total backtracks: %d)", depth, len(s.schedulableItems), s.backtrackCount),
				Metrics: map[string]interface{}{
					"assigned_count":  depth,
					"total_count":     len(s.schedulableItems),
					"backtrack_count": s.backtrackCount,
				},
				Trace: &models.TraceStep{
					Type:  "backtrack",
					Label: fmt.Sprintf("Backtrack item %d", varIdx),
					Score: 0,
				},
			})
		}

		if s.backtrackCount >= s.maxBacktracks {
			return false, nil
		}
	}

	return false, nil
}

// ---------------------------------------------------------------------------
// Forward checking
// ---------------------------------------------------------------------------

// prunedSlot records a slot removed from a domain during forward checking
// so it can be restored on backtrack.
type prunedSlot struct {
	itemIdx  int
	slotIdx  int // position in s.domains[itemIdx] where it was removed from
	slot     Slot
}

// forwardCheck removes from the domains of all unassigned neighbours any slot
// that would conflict with the just-made assignment (varIdx → slot).
// Returns the list of removed slots for restoration on backtrack.
func (s *CPSolver) forwardCheck(varIdx int, assigned Slot, assignment Chromosome) []prunedSlot {
	var pruned []prunedSlot
	item := &s.schedulableItems[varIdx]
	assignedEnd := assigned.BlockIdx + item.Length

	for _, neighbourIdx := range s.constraintGraph[varIdx] {
		if assignment[neighbourIdx].DayIdx != -1 {
			continue // already assigned, skip
		}

		neighbour := &s.schedulableItems[neighbourIdx]
		newDomain := s.domains[neighbourIdx][:0:0] // nil-safe empty slice

		for si, slot := range s.domains[neighbourIdx] {
			conflicts := false
			neighbourEnd := slot.BlockIdx + neighbour.Length

			// Only check overlap on the same day.
			if slot.DayIdx == assigned.DayIdx {
				overlaps := max(slot.BlockIdx, assigned.BlockIdx) < min(neighbourEnd, assignedEnd)
				if overlaps {
					// Instructor clash
					if item.Instructor != "" && item.Instructor == neighbour.Instructor {
						conflicts = true
					}
					// Room clash
					if !conflicts && slot.RoomIdx != -1 && slot.RoomIdx == assigned.RoomIdx {
						conflicts = true
					}
					// Group clash
					if !conflicts {
					outer:
						for _, g1 := range item.AffectedGrps {
							if g1 == "" {
								continue
							}
							for _, g2 := range neighbour.AffectedGrps {
								if g1 == g2 {
									conflicts = true
									break outer
								}
							}
						}
					}
				}
			}

			// Subgroup sync: sub-divisions of the same lesson+distIdx MUST
			// share the same day and block.
			if !conflicts &&
				item.LessonID == neighbour.LessonID &&
				item.DistIdx == neighbour.DistIdx &&
				isSubgroupType(item.LessonDef.Type) {
				if slot.DayIdx != assigned.DayIdx || slot.BlockIdx != assigned.BlockIdx {
					conflicts = true
				}
			}

			if conflicts {
				pruned = append(pruned, prunedSlot{neighbourIdx, si, slot})
			} else {
				newDomain = append(newDomain, slot)
			}
		}

		s.domains[neighbourIdx] = newDomain
	}

	return pruned
}

// restorePruned puts removed slots back into their domains.
// We re-insert each slot and re-sort to keep domains ordered consistently.
func (s *CPSolver) restorePruned(pruned []prunedSlot) {
	// Group by item index for efficient re-insertion.
	byItem := make(map[int][]Slot)
	for _, p := range pruned {
		byItem[p.itemIdx] = append(byItem[p.itemIdx], p.slot)
	}
	for idx, slots := range byItem {
		s.domains[idx] = append(s.domains[idx], slots...)
	}
}

// ---------------------------------------------------------------------------
// Variable selection — MRV + degree heuristic
// ---------------------------------------------------------------------------

// pickVariable returns the index of the unassigned item with the smallest
// remaining domain (MRV). Ties broken by the item with the most constraints
// on other unassigned variables (degree heuristic).
func (s *CPSolver) pickVariable(assignment Chromosome) int {
	bestIdx := -1
	bestDomain := -1
	bestDegree := -1

	for i, gene := range assignment {
		if gene.DayIdx != -1 {
			continue // already assigned
		}
		domainSize := len(s.domains[i])
		if domainSize == 0 {
			return i // wipe-out — pick immediately so we fail fast
		}

		// Count unassigned neighbours (degree).
		degree := 0
		for _, nb := range s.constraintGraph[i] {
			if assignment[nb].DayIdx == -1 {
				degree++
			}
		}

		if bestIdx == -1 ||
			domainSize < bestDomain ||
			(domainSize == bestDomain && degree > bestDegree) {
			bestIdx = i
			bestDomain = domainSize
			bestDegree = degree
		}
	}
	return bestIdx
}

// ---------------------------------------------------------------------------
// Constraint graph
// ---------------------------------------------------------------------------

// buildConstraintGraph precomputes for every item the list of other items
// it shares a binary constraint with (instructor, group, or subgroup sync).
// This avoids scanning all items during forward checking.
func (s *CPSolver) buildConstraintGraph() {
	n := len(s.schedulableItems)
	s.constraintGraph = make([][]int, n)

	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if s.haveConstraint(i, j) {
				s.constraintGraph[i] = append(s.constraintGraph[i], j)
				s.constraintGraph[j] = append(s.constraintGraph[j], i)
			}
		}
	}
}

// haveConstraint returns true if items i and j share any binary constraint.
func (s *CPSolver) haveConstraint(i, j int) bool {
	a := &s.schedulableItems[i]
	b := &s.schedulableItems[j]

	// Instructor clash constraint.
	if a.Instructor != "" && a.Instructor == b.Instructor {
		return true
	}

	// Group clash constraint.
	for _, g1 := range a.AffectedGrps {
		if g1 == "" {
			continue
		}
		for _, g2 := range b.AffectedGrps {
			if g1 == g2 {
				return true
			}
		}
	}

	// Subgroup sync constraint (must share same slot).
	if a.LessonID == b.LessonID &&
		a.DistIdx == b.DistIdx &&
		isSubgroupType(a.LessonDef.Type) {
		return true
	}

	return false
}

// ---------------------------------------------------------------------------
// Consistency check (used during slot iteration)
// ---------------------------------------------------------------------------

// isConsistent checks whether assigning slot to varIdx conflicts with any
// already-assigned variable. This is a safety net; forward checking should
// have already pruned most conflicts from the domain.
func (s *CPSolver) isConsistent(varIdx int, slot Slot, assignment Chromosome) bool {
	item := &s.schedulableItems[varIdx]
	end := slot.BlockIdx + item.Length

	for i, gene := range assignment {
		if i == varIdx || gene.DayIdx == -1 {
			continue
		}
		other := &s.schedulableItems[i]
		otherEnd := gene.BlockIdx + other.Length

		if gene.DayIdx != slot.DayIdx {
			continue
		}

		overlaps := max(slot.BlockIdx, gene.BlockIdx) < min(end, otherEnd)
		if !overlaps {
			continue
		}

		// Instructor clash.
		if item.Instructor != "" && item.Instructor == other.Instructor {
			return false
		}

		// Room clash (only for non-online sessions).
		if slot.RoomIdx != -1 && gene.RoomIdx != -1 && slot.RoomIdx == gene.RoomIdx {
			return false
		}

		// Group clash.
		for _, g1 := range item.AffectedGrps {
			if g1 == "" {
				continue
			}
			for _, g2 := range other.AffectedGrps {
				if g1 == g2 {
					return false
				}
			}
		}
	}

	// Subgroup sync: if another item with the same lessonID+distIdx is already
	// assigned, this item must match its day and block exactly.
	if isSubgroupType(item.LessonDef.Type) {
		for i, gene := range assignment {
			if i == varIdx || gene.DayIdx == -1 {
				continue
			}
			other := &s.schedulableItems[i]
			if other.LessonID == item.LessonID && other.DistIdx == item.DistIdx {
				if gene.DayIdx != slot.DayIdx || gene.BlockIdx != slot.BlockIdx {
					return false
				}
			}
		}
	}

	return true
}

// ---------------------------------------------------------------------------
// Domain initialisation and preference pruning
// ---------------------------------------------------------------------------

func (s *CPSolver) initializeDomains() {
	s.initialDomains = make([][]Slot, len(s.schedulableItems))

	for i, item := range s.schedulableItems {
		maxStart := len(s.blocks) - item.Length
		if maxStart < 0 {
			maxStart = 0
		}
		var domain []Slot
		for d := 0; d < len(s.days); d++ {
			for b := 0; b <= maxStart; b++ {
				if item.Online {
					domain = append(domain, Slot{d, b, -1})
				} else {
					for r := 0; r < len(s.rooms); r++ {
						domain = append(domain, Slot{d, b, r})
					}
				}
			}
		}
		s.initialDomains[i] = domain
	}
}

// pruneDomains removes slots that violate preference and capacity constraints
// from initialDomains before the search begins.
func (s *CPSolver) pruneDomains() error {
	for i, item := range s.schedulableItems {
		pruned := s.initialDomains[i][:0]
		for _, slot := range s.initialDomains[i] {
			if s.isSlotAllowed(item, slot) {
				pruned = append(pruned, slot)
			}
		}
		if len(pruned) == 0 {
			return fmt.Errorf("infeasible: item %s (%s) has no valid slots after pruning preferences",
				item.LessonID, item.LessonDef.Title)
		}
		s.initialDomains[i] = pruned
	}
	return nil
}

func (s *CPSolver) isSlotAllowed(item SchedulableItem, slot Slot) bool {
	// Room capacity.
	if slot.RoomIdx != -1 {
		room := s.rooms[slot.RoomIdx]
		for _, grpID := range item.AffectedGrps {
			if grp, ok := s.groupMap[grpID]; ok && grp.Total > room.Capacity {
				return false
			}
		}
	}

	dayName := s.days[slot.DayIdx]
	startTime := s.blocks[slot.BlockIdx].Start

	// Instructor preferences.
	if inst, ok := s.instructorMap[item.Instructor]; ok {
		if !s.checkPreferences(inst.Preferences, dayName, startTime) {
			return false
		}
	}
	// Group preferences.
	for _, grpID := range item.AffectedGrps {
		if grp, ok := s.groupMap[grpID]; ok {
			if !s.checkPreferences(grp.Preferences, dayName, startTime) {
				return false
			}
		}
	}
	// Unit preferences.
	if unit, ok := s.unitMap[item.Unit]; ok {
		if !s.checkPreferences(unit.Preferences, dayName, startTime) {
			return false
		}
	}

	return true
}

func (s *CPSolver) checkPreferences(prefs []models.Preference, day, startTime string) bool {
	for _, p := range prefs {
		switch p.Type {
		case "ONLY":
			switch p.Target.Kind {
			case "DAY":
				if !equalsFold(day, p.Target.Value) {
					return false
				}
			case "TIME":
				if startTime != p.Target.Value {
					return false
				}
			}
		case "EXCEPT":
			switch p.Target.Kind {
			case "DAY":
				if equalsFold(day, p.Target.Value) {
					return false
				}
			case "TIME":
				if startTime == p.Target.Value {
					return false
				}
			}
		case "BEFORE":
			switch p.Target.Kind {
			case "TIME":
				if !timeBefore(startTime, p.Target.Value) {
					return false
				}
			}
		case "AFTER":
			switch p.Target.Kind {
			case "TIME":
				if !timeAfter(startTime, p.Target.Value) {
					return false
				}
			}
		// Legacy format support.
		case "unavailable_times":
			for _, d := range p.Days {
				if equalsFold(d, day) {
					if len(p.Times) == 0 {
						return false
					}
					for _, t := range p.Times {
						if t == startTime {
							return false
						}
					}
				}
			}
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Partial solution fallback
// ---------------------------------------------------------------------------

// bestPartial fills any unassigned items with the least-conflicting slot
// from their remaining domain, giving the GA the best possible starting
// point when the CP solver hits the backtrack cap.
func (s *CPSolver) bestPartial(assignment Chromosome) Chromosome {
	result := make(Chromosome, len(assignment))
	copy(result, assignment)

	occ := newOccupancyMaps()
	for i, gene := range result {
		if gene.DayIdx != -1 {
			occ.add(&s.schedulableItems[i], gene)
		}
	}

	// Collect unassigned indices and sort by domain size (smallest first)
	// so tightly constrained items get first pick of remaining slots.
	type unassigned struct {
		idx        int
		domainSize int
	}
	var pending []unassigned
	for i, gene := range result {
		if gene.DayIdx == -1 {
			pending = append(pending, unassigned{i, len(s.domains[i])})
		}
	}
	sort.Slice(pending, func(a, b int) bool {
		return pending[a].domainSize < pending[b].domainSize
	})

	gs := &GeneticSolver{
		rooms:            s.rooms,
		days:             s.days,
		blocks:           s.blocks,
		schedulableItems: s.schedulableItems,
		groupMap:         s.groupMap,
	}

	for _, u := range pending {
		item := &s.schedulableItems[u.idx]
		occ.remove(item, result[u.idx]) // no-op if already unassigned

		// Prefer slots still in the pruned domain; fall back to full domain.
		domain := s.domains[u.idx]
		if len(domain) == 0 {
			domain = s.initialDomains[u.idx]
		}

		bestPenalty := -1
		bestGene := Gene{RoomIdx: -1}
		for _, slot := range domain {
			p := occ.conflictCount(item, slot.DayIdx, slot.BlockIdx, slot.RoomIdx)
			if bestPenalty == -1 || p < bestPenalty {
				bestPenalty = p
				bestGene = Gene{slot.DayIdx, slot.BlockIdx, slot.RoomIdx}
				if p == 0 {
					break
				}
			}
		}

		// If the pruned domain gave nothing useful, use greedy placement.
		if bestPenalty > 0 || bestGene.DayIdx == -1 {
			bestGene = gs.bestPlacement(item, occ, 20)
		}

		result[u.idx] = bestGene
		occ.add(item, bestGene)
	}

	return result
}

// ---------------------------------------------------------------------------
// Output
// ---------------------------------------------------------------------------

func (s *CPSolver) formatResponse(c Chromosome, runtime float64) *models.Response {
	gs := &GeneticSolver{
		rooms:            s.rooms,
		days:             s.days,
		blocks:           s.blocks,
		schedulableItems: s.schedulableItems,
		groupMap:         s.groupMap,
		unitMap:          s.unitMap,
		instructorMap:    s.instructorMap,
	}
	gs.buildEvalParams()
	return gs.formatResponse(c, runtime)
}

// ---------------------------------------------------------------------------
// Preprocessing (mirrors GeneticSolver.preprocess)
// ---------------------------------------------------------------------------

func (s *CPSolver) preprocess(req *models.Request) {
	s.rooms = req.Rooms
	s.days = req.Days
	s.blocks = make([]models.Block, 0, len(req.Blocks))
	for _, b := range req.Blocks {
		if b.Type != "break" {
			s.blocks = append(s.blocks, b)
		}
	}

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
					LessonID: l.Identifier, DistIdx: distIdx, Length: count,
					Instructor: l.Instructor, Group: l.Group, Unit: l.Unit,
					Online: l.Online, AffectedGrps: []string{l.Group}, LessonDef: &l,
				})
			case "lesson-merge":
				affected := make([]string, 0, len(l.MultipleIDs))
				for _, m := range l.MultipleIDs {
					affected = append(affected, m.GroupID)
				}
				s.schedulableItems = append(s.schedulableItems, SchedulableItem{
					LessonID: l.Identifier, DistIdx: distIdx, Length: count,
					Instructor: l.Instructor, Group: "", Unit: l.Unit,
					Online: l.Online, AffectedGrps: affected, LessonDef: &l,
				})
			case "subgroup", "subgroup-lesson-merge":
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

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func deepCopyDomains(src [][]Slot) [][]Slot {
	dst := make([][]Slot, len(src))
	for i, d := range src {
		dst[i] = make([]Slot, len(d))
		copy(dst[i], d)
	}
	return dst
}

func isSubgroupType(t string) bool {
	return t == "subgroup" || t == "subgroup-lesson-merge"
}

// equalsFold is a simple case-insensitive string compare for day names.
func equalsFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 32
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 32
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// timeBefore returns true if time string a is strictly before b.
// Times are expected in "H:MMam/pm" format (e.g. "9:00am", "12:00pm").
func timeBefore(a, b string) bool {
	return timeToMinutes(a) < timeToMinutes(b)
}

func timeAfter(a, b string) bool {
	return timeToMinutes(a) > timeToMinutes(b)
}

func timeToMinutes(t string) int {
	if len(t) < 6 {
		return 0
	}
	isPM := t[len(t)-2:] == "pm"
	isAM := t[len(t)-2:] == "am"
	_ = isAM
	core := t[:len(t)-2] // strip "am"/"pm"

	hour, min := 0, 0
	for i, ch := range core {
		if ch == ':' {
			// parse hour up to colon, min after
			for _, c := range core[:i] {
				hour = hour*10 + int(c-'0')
			}
			for _, c := range core[i+1:] {
				min = min*10 + int(c-'0')
			}
			break
		}
	}

	if isPM && hour != 12 {
		hour += 12
	}
	if !isPM && hour == 12 { // 12:00am = midnight
		hour = 0
	}
	return hour*60 + min
}