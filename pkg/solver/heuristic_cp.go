package solver

import (
	"context"
	"fmt"
	"geliana-go/pkg/evaluator"
	"geliana-go/pkg/models"
	"math/rand"
	"sort"
	"strings"
	"time"
)

type VariableOrderMode int

const (
	Sequential VariableOrderMode = iota
	MRV
	MRVDegree
)

type ValueOrderMode int

const (
	SequentialValue ValueOrderMode = iota
	LeastLoadedDay
)

func (m VariableOrderMode) String() string {
	switch m {
	case Sequential:
		return "Sequential"
	case MRV:
		return "MRV"
	case MRVDegree:
		return "MRVDegree"
	default:
		return "Unknown"
	}
}

func (m ValueOrderMode) String() string {
	switch m {
	case SequentialValue:
		return "Sequential"
	case LeastLoadedDay:
		return "LeastLoadedDay"
	default:
		return "Unknown"
	}
}

type HeuristicCPSolver struct {
	containers  []Container
	assignments []Assignment
	rooms       []models.Room
	days        []string
	lessons     []models.Lesson
	groups      []models.Group
	units       []models.Unit
	instructors []models.Instructor
	occ         occupancyDictonary
	blocks      []models.Block

	optimiseDefaultRooms bool
	optimisePreferences  bool
	optimiseRoomCapacity bool

	seed        int64
	shuffleMode string
	restarts    int
	effectiveSeed int64
	rng           *rand.Rand
	attempt      int
	attemptsDone int
	roomIndexByID        map[string]int
	groupByID            map[string]models.Group
	unitByID             map[string]models.Unit
	instructorByID       map[string]models.Instructor

	neighbors [][]int

	VariableOrder VariableOrderMode
	ValueOrder    ValueOrderMode

	backtrackCalls int

	relaxLessonDay bool

	spreadMode string

	maxBacktracks int

	progressLastUpdate time.Time

	evalParams *evaluator.EvaluationParams
}

const heuristicProgressInterval = 200 * time.Millisecond

func NewHeuristicCPSolver(varOrder VariableOrderMode, valOrder ValueOrderMode) *HeuristicCPSolver {
	return &HeuristicCPSolver{
		VariableOrder: varOrder,
		ValueOrder:    valOrder,
	}
}

func normalizeShuffleMode(m string) string {
	switch strings.ToLower(strings.TrimSpace(m)) {
	case "off":
		return "off"
	case "full":
		return "full"
	default:
		return "ties"
	}
}

func (s *HeuristicCPSolver) applyShuffle(idx []int, key func(int) [2]int) {
	if s.rng == nil || len(idx) < 2 {
		return
	}
	switch s.shuffleMode {
	case "full":
		s.rng.Shuffle(len(idx), func(a, b int) {
			idx[a], idx[b] = idx[b], idx[a]
		})
	case "ties":
		for start := 0; start < len(idx); {
			end := start + 1
			k0 := key(idx[start])
			for end < len(idx) && key(idx[end]) == k0 {
				end++
			}
			s.rng.Shuffle(end-start, func(a, b int) {
				idx[start+a], idx[start+b] = idx[start+b], idx[start+a]
			})
			start = end
		}
	}
}

func (s *HeuristicCPSolver) Solve(ctx context.Context, req *models.Request, tracker ProgressTracker) (*models.Response, error) {
	startTime := time.Now()
	s.preprocess(req)
	if len(s.containers) == 0 {
		return nil, fmt.Errorf("no schedulable items found")
	}

	s.seed = req.Seed
	s.shuffleMode = normalizeShuffleMode(req.Shuffle)
	s.restarts = req.Restarts
	if s.restarts < 1 {
		s.restarts = 1
	}
	s.effectiveSeed = s.seed
	if s.effectiveSeed == 0 {
		s.effectiveSeed = time.Now().UnixNano()
	}
	master := rand.New(rand.NewSource(s.effectiveSeed))

	if tracker != nil {
		tracker.Update(ProgressReport{
			CurrentStep: 0,
			StepLabel:   "config",
			Metrics: map[string]interface{}{
				"variable_ordering":      s.VariableOrder.String(),
				"value_ordering":         s.ValueOrder.String(),
				"optimise_default_rooms": req.OptimiseDefaultRooms,
				"optimise_room_capacity": req.OptimiseRoomCapacity,
				"optimise_preferences":   req.OptimisePreferences,
				"mode":                   req.Mode,
				"containers":             len(s.containers),
				"days":                   len(s.days),
				"rooms":                  len(s.rooms),
				"blocks":                 len(s.blocks),
				"backtrack_calls":        s.backtrackCalls,
				"seed":                   s.effectiveSeed,
				"shuffle":                s.shuffleMode,
				"restarts":               s.restarts,
			},
		})
	}

	s.buildEvalParams()

	var best *models.Response
	bestScore := -1.0
	var lastErr error
	for attempt := 1; attempt <= s.restarts; attempt++ {
		if ctx.Err() != nil {
			break
		}
		s.attempt = attempt
		s.attemptsDone = attempt
		s.rng = rand.New(rand.NewSource(master.Int63()))
		resp, err := s.solveOnce(ctx, req, tracker)
		if err != nil {
			lastErr = err
			continue
		}
		if best == nil || resp.Stats.OverallScore > bestScore {
			best = resp
			bestScore = resp.Stats.OverallScore
		}
		if bestScore >= 100 {
			break
		}
	}

	if best == nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, fmt.Errorf("no clash-free solution found (backtrackCalls=%d)", s.backtrackCalls)
	}

	best.Stats.TimeTaken = time.Since(startTime).Seconds()

	if tracker != nil {
		s.reportProgress(tracker, len(s.containers), len(s.containers), "Solution found", true)
	}

	return best, nil
}

func (s *HeuristicCPSolver) solveOnce(ctx context.Context, req *models.Request, tracker ProgressTracker) (*models.Response, error) {
	attemptStart := time.Now()

	s.assignments = make([]Assignment, len(s.containers))
	for i := range s.assignments {
		s.assignments[i] = Assignment{DayIdx: -1, TimeIdx: -1, RoomIdx: nil}
	}
	s.occ = *newOccupancyDictonary()

	s.spreadMode = "strict"
	s.relaxLessonDay = false
	s.maxBacktracks = heuristicPass1BudgetPerContainer * len(s.containers)
	if s.strictSpreadImpossible() {
		s.relaxLessonDay = true
		s.spreadMode = "relaxed"
		s.maxBacktracks = 0
	}

	assigned := make([]bool, len(s.containers))
	solved := s.backtrack(ctx, assigned, len(s.containers), tracker)

	if !solved && ctx.Err() == nil && !s.relaxLessonDay {
		s.relaxLessonDay = true
		s.spreadMode = "relaxed"
		s.maxBacktracks = 0
		assigned = s.resetForRelaxedPass()
		solved = s.backtrack(ctx, assigned, len(s.containers), tracker)
	}

	if !solved {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("no clash-free solution found (backtrackCalls=%d)", s.backtrackCalls)
	}

	return s.formatResponse(time.Since(attemptStart).Seconds()), nil
}

const heuristicPass1BudgetPerContainer = 100

func (s *HeuristicCPSolver) resetForRelaxedPass() []bool {
	assigned := make([]bool, len(s.containers))
	s.assignments = make([]Assignment, len(s.containers))
	for i := range s.assignments {
		s.assignments[i] = Assignment{DayIdx: -1, TimeIdx: -1, RoomIdx: nil}
	}
	s.occ = *newOccupancyDictonary()
	return assigned
}

func (s *HeuristicCPSolver) strictSpreadImpossible() bool {
	slotsPerLesson := make(map[string]map[int]bool)
	for _, c := range s.containers {
		set := slotsPerLesson[c.LessonID]
		if set == nil {
			set = make(map[int]bool)
			slotsPerLesson[c.LessonID] = set
		}
		set[c.DistIdx] = true
	}
	for _, set := range slotsPerLesson {
		if len(set) > len(s.days) {
			return true
		}
	}
	return false
}

func (s *HeuristicCPSolver) reportProgress(tracker ProgressTracker, placed, total int, label string, force bool) {
	if tracker == nil {
		return
	}
	if !force {
		if time.Since(s.progressLastUpdate) < heuristicProgressInterval {
			return
		}
	}
	s.progressLastUpdate = time.Now()

	fitness := 0.0
	if total > 0 {
		fitness = float64(placed) / float64(total) * 100.0
	}
	tracker.Update(ProgressReport{
		CurrentStep: placed,
		TotalSteps:  total,
		StepLabel:   label,
		BestFitness: fitness,
		Metrics: map[string]interface{}{
			"assigned_count":  placed,
			"total_count":     total,
			"backtrack_count": s.backtrackCalls,
			"best_fitness":    fitness,
			"spread_mode":     s.spreadMode,
		},
		Trace: &models.TraceStep{
			Type:  "assignment",
			Label: label,
			Score: fitness,
		},
	})
}

func (s *HeuristicCPSolver) selectNextVariable(assigned []bool) int {
	if s.VariableOrder == Sequential {
		for i, done := range assigned {
			if !done {
				return i
			}
		}
		return -1
	}

	best := -1
	bestDomain := -1
	bestDegree := -1

	for i, done := range assigned {
		if done {
			continue
		}

		domain := s.domainSize(&s.containers[i])

		if domain == 0 {
			return i
		}

		useThis := false
		switch {
		case best == -1:
			useThis = true
		case domain < bestDomain:
			useThis = true
		case domain == bestDomain && s.VariableOrder == MRVDegree:
			degree := s.unassignedDegree(i, assigned)
			if degree > bestDegree {
				useThis = true
				bestDegree = degree
			}
		}

		if useThis {
			best = i
			bestDomain = domain
			if s.VariableOrder == MRVDegree {
				bestDegree = s.unassignedDegree(i, assigned)
			}
		}
	}

	return best
}

func (s *HeuristicCPSolver) domainSize(item *Container) int {
	needed := roomsNeeded(item)
	noPreference := make([]int, needed)
	for i := range noPreference {
		noPreference[i] = -1
	}

	count := 0
	for dayIdx := 0; dayIdx < len(s.days); dayIdx++ {
		if !s.relaxLessonDay && s.occ.lessonDayUsed(item, dayIdx) {
			continue
		}
		for blockIdx := range s.blocks {
			if !s.blocksAvailable(blockIdx, item.Length) {
				continue
			}
			if _, ok := s.findRoomCombo(item, dayIdx, blockIdx, needed, noPreference); ok {
				count++
			}
		}
	}
	return count
}

func (s *HeuristicCPSolver) unassignedDegree(index int, assigned []bool) int {
	degree := 0
	for _, n := range s.neighbors[index] {
		if !assigned[n] {
			degree++
		}
	}
	return degree
}

func (s *HeuristicCPSolver) orderedDays(item *Container) []int {
	days := make([]int, len(s.days))
	for i := range days {
		days[i] = i
	}

	load := make([]int, len(s.days))
	if s.ValueOrder == LeastLoadedDay {
		for _, grp := range item.AffectedGrps {
			for k := range s.occ.group[grp] {
				load[k.day]++
			}
		}
	}

	violations := make([]int, len(s.days))
	if s.optimisePreferences {
		prefs := filterPrefsByKind(RelevantPreferences(item, s.groupByID, s.unitByID, s.instructorByID), KindDay)
		for d := range s.days {
			violations[d] = ViolationCount(prefs, s.days, Candidate{DayName: s.days[d]})
		}
	}

	sort.SliceStable(days, func(a, b int) bool {
		va, vb := violations[days[a]], violations[days[b]]
		if va != vb {
			return va < vb
		}
		return load[days[a]] < load[days[b]]
	})
	s.applyShuffle(days, func(d int) [2]int { return [2]int{violations[d], load[d]} })
	return days
}

func (s *HeuristicCPSolver) orderedBlocks(item *Container, dayIdx int) []int {
	blocks := make([]int, len(s.blocks))
	for i := range blocks {
		blocks[i] = i
	}
	violations := make([]int, len(s.blocks))
	if s.optimisePreferences {
		if prefs := filterPrefsByKind(RelevantPreferences(item, s.groupByID, s.unitByID, s.instructorByID), KindTime); len(prefs) > 0 {
			dayName := ""
			if dayIdx >= 0 && dayIdx < len(s.days) {
				dayName = s.days[dayIdx]
			}
			for b := range s.blocks {
				violations[b] = ViolationCount(prefs, s.days, Candidate{DayName: dayName, Time: s.blocks[b].Start})
			}
		}
	}
	sort.SliceStable(blocks, func(a, b int) bool {
		return violations[blocks[a]] < violations[blocks[b]]
	})
	s.applyShuffle(blocks, func(b int) [2]int { return [2]int{violations[b], 0} })
	return blocks
}

func filterPrefsByKind(prefs []models.Preference, kind string) []models.Preference {
	out := make([]models.Preference, 0, len(prefs))
	for _, p := range prefs {
		if p.Target.Kind == kind {
			out = append(out, p)
		}
	}
	return out
}

func (s *HeuristicCPSolver) preferredRoomIdx(groupID, unitID, lessonDefault string) int {
	if lessonDefault != "" {
		if idx, ok := s.roomIndexByID[lessonDefault]; ok {
			return idx
		}
	}
	if u, ok := s.unitByID[unitID]; ok && u.DefaultRoom != "" {
		if idx, ok := s.roomIndexByID[u.DefaultRoom]; ok {
			return idx
		}
	}
	if g, ok := s.groupByID[groupID]; ok && g.DefaultRoom != "" {
		if idx, ok := s.roomIndexByID[g.DefaultRoom]; ok {
			return idx
		}
	}
	return -1
}

func (s *HeuristicCPSolver) preferredRoomsFor(item *Container, needed int) []int {
	preferred := make([]int, needed)
	for i := range preferred {
		preferred[i] = -1
	}
	if !s.optimiseDefaultRooms || needed == 0 {
		return preferred
	}

	switch item.LessonDef.Type {
	case "lesson-merge", "subgroup", "subgroup-lesson-merge":
		for i, m := range item.LessonDef.MultipleIDs {
			if i >= needed {
				break
			}
			preferred[i] = s.preferredRoomIdx(m.GroupID, m.UnitID, item.LessonDef.DefaultRoom)
		}
	default:
		preferred[0] = s.preferredRoomIdx(item.Group, item.Unit, item.LessonDef.DefaultRoom)
	}
	return preferred
}

func (s *HeuristicCPSolver) blocksAvailable(blockIdx, length int) bool {
	return blockIdx+length <= len(s.blocks)
}

func (s *HeuristicCPSolver) findRoomCombo(item *Container, dayIdx, blockIdx, needed int, preferred []int) ([]int, bool) {
	if needed == 0 {
		return []int{}, true
	}

	roomOrder := s.orderedRoomCandidates(item)

	chosen := make([]int, 0, needed)
	used := make(map[int]bool, needed)

	var search func() bool
	search = func() bool {
		if len(chosen) == needed {
			return true
		}

		slot := len(chosen)
		if pref := preferred[slot]; pref >= 0 && !used[pref] {
			if s.occ.conflictCount(item, dayIdx, blockIdx, []int{pref}) == 0 {
				chosen = append(chosen, pref)
				used[pref] = true
				if search() {
					return true
				}
				chosen = chosen[:len(chosen)-1]
				delete(used, pref)
			}
		}

		for _, r := range roomOrder {
			if used[r] {
				continue
			}
			if s.occ.conflictCount(item, dayIdx, blockIdx, []int{r}) == 0 {
				chosen = append(chosen, r)
				used[r] = true
				if search() {
					return true
				}
				chosen = chosen[:len(chosen)-1]
				delete(used, r)
			}
		}
		return false
	}

	ok := search()
	return chosen, ok
}

// orderedRoomCandidates returns room indices in the order findRoomCombo
// should try them for THIS container: rooms that fit every affected group
// first (when optimiseRoomCapacity is on), then fewest ROOM-kind preference
// violations (when optimisePreferences is on), then LEAST USED SO FAR as an
// always-on baseline tiebreak -- unlike capViol/roomViol, usage is computed
// unconditionally, regardless of which optimise flags are set.
//
// This last part matters specifically for shuffleMode="ties": ties only
// shuffles candidates that score EQUALLY, which is meant to be safe because
// it can never discard a real distinction the heuristic already made. But
// with both optimise flags off, capViol and roomViol are zero for every
// room -- meaning the WHOLE room list would count as one tied group, and
// "ties" mode would end up shuffling all of it, indistinguishable from
// "full". The usage tiebreak exists so there is ALWAYS some real signal
// underneath capViol/roomViol: two rooms only tie now if they're genuinely
// equally used, not just because neither optimise flag happened to be on.
// Soft ordering only throughout -- feasibility is unchanged; a room this
// pushes to the back is still tried, just later.
func (s *HeuristicCPSolver) orderedRoomCandidates(item *Container) []int {
	order := make([]int, len(s.rooms))
	for i := range order {
		order[i] = i
	}
	var roomPrefs []models.Preference
	if s.optimisePreferences {
		roomPrefs = filterPrefsByKind(RelevantPreferences(item, s.groupByID, s.unitByID, s.instructorByID), KindRoom)
	}
	capViol := make([]int, len(s.rooms))
	roomViol := make([]int, len(s.rooms))
	usage := make([]int, len(s.rooms))
	for r := range s.rooms {
		capViol[r] = s.roomCapacityOverflow(item, r)
		if len(roomPrefs) > 0 {
			roomViol[r] = ViolationCount(roomPrefs, s.days, Candidate{RoomIDs: []string{s.rooms[r].ID}})
		}
		// How many (day, block) slots this room already holds, across the
		// whole search so far -- read straight from the occupancy map (its
		// own map length), no extra bookkeeping needed. Always computed,
		// never gated behind a flag.
		usage[r] = len(s.occ.room[r])
	}
	sort.SliceStable(order, func(a, b int) bool {
		// Physical fit and explicit preferences still dominate, in that
		// order -- usage only ever breaks a tie between rooms that are
		// otherwise indistinguishable, it never overrides a real ask.
		if capViol[order[a]] != capViol[order[b]] {
			return capViol[order[a]] < capViol[order[b]]
		}
		if roomViol[order[a]] != roomViol[order[b]] {
			return roomViol[order[a]] < roomViol[order[b]]
		}
		return usage[order[a]] < usage[order[b]]
	})
	// applyShuffle's key is a fixed [2]int, shared with orderedDays/
	// orderedBlocks, so capViol and roomViol -- the two EXPLICIT, opt-in
	// signals -- are packed into one combined number here (capViol dominates
	// via the *1000 multiplier, which only needs to comfortably exceed the
	// largest roomViol can realistically be), with usage kept as the second,
	// lower-priority slot underneath them.
	s.applyShuffle(order, func(r int) [2]int {
		return [2]int{capViol[r]*1000 + roomViol[r], usage[r]}
	})
	return order
}

func (s *HeuristicCPSolver) roomCapacityOverflow(item *Container, roomIdx int) int {
	if !s.optimiseRoomCapacity || item.Online {
		return 0
	}
	if roomIdx < 0 || roomIdx >= len(s.rooms) {
		return 0
	}
	room := s.rooms[roomIdx]
	overflow := 0
	for _, grpID := range item.AffectedGrps {
		if grpID == "" {
			continue
		}
		if g, ok := s.groupByID[grpID]; ok && g.Total > room.Capacity {
			overflow++
		}
	}
	return overflow
}

func (s *HeuristicCPSolver) backtrack(ctx context.Context, assigned []bool, remainingCount int, tracker ProgressTracker) bool {
	select {
	case <-ctx.Done():
		return false
	default:
	}

	if remainingCount == 0 {
		return true
	}

	if s.maxBacktracks > 0 && s.backtrackCalls >= s.maxBacktracks {
		return false
	}

	s.backtrackCalls++

	total := len(s.containers)
	placed := total - remainingCount
	label := fmt.Sprintf("Placing %d/%d [%s] (backtracks: %d)", placed+1, total, s.spreadMode, s.backtrackCalls)
	if s.restarts > 1 {
		label = fmt.Sprintf("Placing %d/%d [attempt %d/%d, %s] (backtracks: %d)", placed+1, total, s.attempt, s.restarts, s.spreadMode, s.backtrackCalls)
	}
	s.reportProgress(tracker, placed, total, label, false)

	index := s.selectNextVariable(assigned)
	item := &s.containers[index]
	needed := roomsNeeded(item)
	preferred := s.preferredRoomsFor(item, needed)

	for _, dayIdx := range s.orderedDays(item) {
		select {
		case <-ctx.Done():
			return false
		default:
		}
		if !s.relaxLessonDay && s.occ.lessonDayUsed(item, dayIdx) {
			continue
		}

		for _, blockIdx := range s.orderedBlocks(item, dayIdx) {
			select {
			case <-ctx.Done():
				return false
			default:
			}
			if !s.blocksAvailable(blockIdx, item.Length) {
				continue
			}

			if s.occ.conflictCount(item, dayIdx, blockIdx, []int{}) > 0 {
				continue
			}

			roomCombo, ok := s.findRoomCombo(item, dayIdx, blockIdx, needed, preferred)
			if !ok {
				continue
			}

			assignment := Assignment{DayIdx: dayIdx, TimeIdx: blockIdx, RoomIdx: roomCombo}

			s.assignments[index] = assignment
			s.occ.add(item, assignment)
			assigned[index] = true

			if s.backtrack(ctx, assigned, remainingCount-1, tracker) {
				return true
			}

			s.occ.remove(item, assignment)
			s.assignments[index] = Assignment{DayIdx: -1, TimeIdx: -1, RoomIdx: nil}
			assigned[index] = false
		}
	}

	return false
}

func (s *HeuristicCPSolver) preprocess(req *models.Request) {
	s.rooms = req.Rooms
	s.days = req.Days
	if len(s.days) == 0 {
		s.days = req.Configuration.Days
	}
	s.blocks = make([]models.Block, 0, len(req.Blocks))
	for _, b := range req.Blocks {
		if b.Type != "break" {
			s.blocks = append(s.blocks, b)
		}
	}
	s.lessons = req.Lessons
	s.groups = req.Groups
	s.units = req.Units
	s.instructors = req.Instructors
	s.occ = *newOccupancyDictonary()
	s.optimiseDefaultRooms = req.OptimiseDefaultRooms
	s.optimisePreferences = req.OptimisePreferences
	s.optimiseRoomCapacity = req.OptimiseRoomCapacity

	s.instructorByID = make(map[string]models.Instructor, len(s.instructors))
	for _, inst := range s.instructors {
		s.instructorByID[inst.ID] = inst
	}

	s.roomIndexByID = make(map[string]int, len(s.rooms))
	for i, r := range s.rooms {
		s.roomIndexByID[r.ID] = i
	}
	s.groupByID = make(map[string]models.Group, len(s.groups))
	for _, g := range s.groups {
		s.groupByID[g.ID] = g
	}
	s.unitByID = make(map[string]models.Unit, len(s.units))
	for _, u := range s.units {
		s.unitByID[u.ID] = u
	}

	for li := range req.Lessons {
		l := req.Lessons[li]
		for distIdx, distribution := range l.Distribution {
			if distribution == 0 {
				continue
			}

			switch l.Type {
			case "regular":
				s.containers = append(s.containers, Container{
					LessonID: l.Identifier, DistIdx: distIdx, Length: distribution,
					Instructor: l.Instructor, Group: l.Group, Unit: l.Unit, Online: l.Online,
					AffectedGrps: []string{l.Group}, LessonDef: &req.Lessons[li],
				})
			case "lesson-merge":
				affected := make([]string, 0, len(l.MultipleIDs))
				for _, m := range l.MultipleIDs {
					affected = append(affected, m.GroupID)
				}
				s.containers = append(s.containers, Container{
					LessonID: l.Identifier, DistIdx: distIdx, Length: distribution,
					Instructor: l.Instructor, Group: "", Unit: l.Unit, Online: l.Online,
					AffectedGrps: affected, LessonDef: &req.Lessons[li],
				})
			case "subgroup":
				s.containers = append(s.containers, Container{
					LessonID: l.Identifier, DistIdx: distIdx, Length: distribution,
					Group: l.Group, Unit: l.Unit, Online: l.Online,
					AffectedGrps: []string{l.Group}, LessonDef: &req.Lessons[li],
				})
			case "subgroup-lesson-merge":
				affected := make([]string, 0, len(l.MultipleIDs))
				for _, m := range l.MultipleIDs {
					affected = append(affected, m.GroupID)
				}
				s.containers = append(s.containers, Container{
					LessonID: l.Identifier, DistIdx: distIdx, Length: distribution,
					Group: l.Group, Unit: l.Unit, Online: l.Online,
					AffectedGrps: affected, LessonDef: &req.Lessons[li],
				})
			}

			s.assignments = append(s.assignments, Assignment{DayIdx: -1, TimeIdx: -1, RoomIdx: nil})
		}
	}

	s.buildNeighbors()
}

func (s *HeuristicCPSolver) buildNeighbors() {
	n := len(s.containers)
	s.neighbors = make([][]int, n)

	byGroup := make(map[string][]int)
	byInstr := make(map[string][]int)

	for i := range s.containers {
		c := &s.containers[i]
		for _, g := range c.AffectedGrps {
			if g != "" {
				byGroup[g] = append(byGroup[g], i)
			}
		}
		for _, inst := range instructorIDs(c) {
			if inst != "" {
				byInstr[inst] = append(byInstr[inst], i)
			}
		}
	}

	seen := make([]map[int]bool, n)
	for i := range seen {
		seen[i] = make(map[int]bool)
	}

	addEdgesWithin := func(group []int) {
		for _, a := range group {
			for _, b := range group {
				if a != b && !seen[a][b] {
					seen[a][b] = true
					s.neighbors[a] = append(s.neighbors[a], b)
				}
			}
		}
	}

	for _, g := range byGroup {
		addEdgesWithin(g)
	}
	for _, g := range byInstr {
		addEdgesWithin(g)
	}
}

func (s *HeuristicCPSolver) buildEvalParams() {
	periodTimes := make([]string, len(s.blocks))
	for i, block := range s.blocks {
		periodTimes[i] = block.Start
	}
	s.evalParams = &evaluator.EvaluationParams{
		Days: s.days, PeriodTimes: periodTimes, Lessons: s.lessons, Rooms: s.rooms,
		Groups: s.groups, Instructors: s.instructors, Units: s.units, Blocks: make([]models.Block, 0),
	}
}

func (s *HeuristicCPSolver) formatResponse(runtime float64) *models.Response {
	sessions := make([]models.Session, 0, len(s.containers))

	for i, container := range s.containers {
		assignment := s.assignments[i]
		def := container.LessonDef

		day := ""
		if assignment.DayIdx >= 0 && assignment.DayIdx < len(s.days) {
			day = s.days[assignment.DayIdx]
		}
		timeStr := ""
		if assignment.TimeIdx >= 0 && assignment.TimeIdx < len(s.blocks) {
			timeStr = s.blocks[assignment.TimeIdx].Start
		}
		room := ""
		if !container.Online && len(assignment.RoomIdx) > 0 {
			names := make([]string, 0, len(assignment.RoomIdx))
			for _, r := range assignment.RoomIdx {
				if r >= 0 && r < len(s.rooms) {
					names = append(names, s.rooms[r].ID)
				}
			}
			room = strings.Join(names, ",")
		}

		sess := models.Session{
			Day: day, Time: timeStr, Room: room, Unit: container.Unit, Online: container.Online,
			Blocks: container.Length, Multiple: def.Multiple, Type: def.Type, Short: def.Short,
			Title: def.Title, DatabaseIDs: def.DatabaseIDs, TimetableID: def.TimetableID,
		}

		switch def.Type {
		case "regular":
			sess.Group = container.Group
			sess.Instructor = container.Instructor
			sess.Identifier = fmt.Sprintf("%s-%s-%s-%d", def.Identifier, container.Group, container.Instructor, i)

		case "lesson-merge":
			sess.Instructor = container.Instructor
			sess.AffectedGroups = container.AffectedGrps
			sess.MultipleIDs = withAssignedRooms(def.MultipleIDs, assignment.RoomIdx, s.rooms, container.Online)
			sess.Identifier = fmt.Sprintf("%s-%s-%d", def.Identifier, container.Instructor, i)

		case "subgroup", "subgroup-lesson-merge":
			sess.Group = container.Group
			sess.AffectedGroups = container.AffectedGrps
			sess.MultipleIDs = withAssignedRooms(def.MultipleIDs, assignment.RoomIdx, s.rooms, container.Online)
			instructors := make([]string, 0, len(def.MultipleIDs))
			for _, m := range def.MultipleIDs {
				instructors = append(instructors, m.InstID)
			}
			sess.AffectedInstructors = instructors
			sess.Identifier = fmt.Sprintf("%s-%s-%d", def.Identifier, container.Group, i)

		default:
			continue
		}

		sessions = append(sessions, sess)
	}

	evalResult := evaluator.Evaluate(sessions, s.evalParams)

	return &models.Response{
		Error: false, Message: fmt.Sprintf("Heuristic CP | varOrder=%s valOrder=%s | spread=%s | seed=%d shuffle=%s attempts=%d/%d | backtrackCalls=%d", s.VariableOrder, s.ValueOrder, s.spreadMode, s.effectiveSeed, s.shuffleMode, s.attemptsDone, s.restarts, s.backtrackCalls),
		Sessions: sessions,
		Stats: models.OptimizationStats{
			OverallScore:      evalResult.OverallScore,
			TimeTaken:         runtime,
			SolutionFound:     true,
			HardScore:         evalResult.HardScore,
			PreferenceScore:   evalResult.PreferenceScore,
			DistributionScore: evalResult.DistributionScore,
			DefaultRoomScore:  evalResult.DefaultRoomScore,
			HiddenSessions:    evalResult.HiddenSessions,
			TotalClashes:      evalResult.TotalClashes,
			Health:            evalResult.Health,
		},
	}
}