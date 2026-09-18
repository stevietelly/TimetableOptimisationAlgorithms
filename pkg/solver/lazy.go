package solver

import (
	"context"
	"fmt"
	"geliana-go/pkg/evaluator"
	"geliana-go/pkg/models"
	"strings"
	"time"
)

// Lazy solver is my attempt to understand golang and how the models are built
// mostly for debugging and understandng how and why ai wrote this code

type LazySolver struct {
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
	roomIndexByID        map[string]int
	groupByID            map[string]models.Group
	unitByID             map[string]models.Unit

	evalParams *evaluator.EvaluationParams
}

func NewLazySolver() *LazySolver {
	return &LazySolver{}
}

func (s *LazySolver) Solve(ctx context.Context, req *models.Request, tracker ProgressTracker) (*models.Response, error) {
	startTime := time.Now()
	s.preprocess(req)
	if len(s.containers) == 0 {
		return nil, fmt.Errorf("no schedulable items found")
	}

	s.buildEvalParams()

	if !s.backtrack(0) {
		return nil, fmt.Errorf("no clash-free solution found")
	}

	return s.formatResponse(time.Since(startTime).Seconds()), nil
}

// preferredRoomIdx resolves a single (group, unit) pair down to a room index,
// following the priority order lesson -> unit -> group. lessonDefault is
// passed in separately since it's shared across every division of a
// lesson-merge/subgroup lesson, not looked up per-division.

func (s *LazySolver) preferredRoomIdx(groupID, unitID, lessonDefault string) int {
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

// preferredRoomsFor returns one preferred room index per room this container
// needs (see roomsNeeded), in the same order findRoomCombo fills slots in.
// A -1 entry means "no preference for this slot" -- findRoomCombo falls back
// to its normal exhaustive search for that slot instead.
func (s *LazySolver) preferredRoomsFor(item *Container, needed int) []int {
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
	default: // "regular"
		preferred[0] = s.preferredRoomIdx(item.Group, item.Unit, item.LessonDef.DefaultRoom)
	}
	return preferred
}

// roomsNeeded returns how many DISTINCT rooms this container needs at once.
// regular -> 1 (or 0 if online). lesson-merge / subgroup -> one room per
// division/merge component, since they happen simultaneously in different rooms.
func roomsNeeded(item *Container) int {
	if item.Online {
		return 0
	}
	switch item.LessonDef.Type {
	case "lesson-merge", "subgroup", "subgroup-lesson-merge":
		if n := len(item.LessonDef.MultipleIDs); n > 0 {
			return n
		}
	}
	return 1
}

// blocksAvailable checks that the span [blockIdx, blockIdx+length) fits
// within s.blocks. s.blocks contains only period blocks (break blocks are
// filtered out in preprocess), so no additional type check is needed here.
func (s *LazySolver) blocksAvailable(blockIdx, length int) bool {
	return blockIdx+length <= len(s.blocks)
}

// findRoomCombo is its own small backtracking search: pick `needed` distinct
// rooms with zero conflict, trying one, recursing to pick the next, and
// undoing (removing from `used`/`chosen`) if that path doesn't pan out --
// same commit/recurse/undo shape as backtrack itself, just scoped to rooms.
//
// preferred[slot], if >= 0, is tried BEFORE the general 0..len(rooms) sweep
// for that slot -- a soft nudge, not a hard requirement: if the preferred
// room is already taken or otherwise conflicts, the search just falls
// through to everything else exactly as before.
func (s *LazySolver) findRoomCombo(item *Container, dayIdx, blockIdx, needed int, preferred []int) ([]int, bool) {
	if needed == 0 {
		return []int{}, true
	}

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
		for r := 0; r < len(s.rooms); r++ {
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

func (s *LazySolver) backtrack(index int) bool {
	if index == len(s.containers) {
		return true
	}

	item := &s.containers[index]
	needed := roomsNeeded(item)
	preferred := s.preferredRoomsFor(item, needed) // per-item, doesn't depend on day/block -- compute once

	for dayIdx := 0; dayIdx < len(s.days); dayIdx++ {
		if s.occ.lessonDayUsed(item, dayIdx) {
			continue // this lesson already has a session on this day
		}

		for blockIdx := range s.blocks {
			if !s.blocksAvailable(blockIdx, item.Length) {
				continue
			}

			// Explicit group/instructor conflict check before room assignment.
			// Online sessions bypass findRoomCombo's internal conflictCount,
			// so we guard here for all sessions with an empty room list.
			if s.occ.conflictCount(item, dayIdx, blockIdx, []int{}) > 0 {
				continue
			}

			roomCombo, ok := s.findRoomCombo(item, dayIdx, blockIdx, needed, preferred)
			if !ok {
				continue // no room combination works at this slot
			}

			assignment := Assignment{DayIdx: dayIdx, TimeIdx: blockIdx, RoomIdx: roomCombo}

			s.assignments[index] = assignment
			s.occ.add(item, assignment)

			if s.backtrack(index + 1) {
				return true
			}

			s.occ.remove(item, assignment)
			s.assignments[index] = Assignment{DayIdx: -1, TimeIdx: -1, RoomIdx: nil}
		}
	}

	return false
}

func (s *LazySolver) preprocess(req *models.Request) {
	s.rooms = req.Rooms
	s.days = req.Days
	if len(s.days) == 0 {
		s.days = req.Configuration.Days // some datasets nest days under configuration instead of root
	}
	// Only keep schedulable period blocks — break blocks must never receive
	// sessions. The blocks array in the request mixes both types.
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
					LessonID:     l.Identifier,
					DistIdx:      distIdx,
					Length:       distribution,
					Instructor:   l.Instructor,
					Group:        l.Group,
					Unit:         l.Unit,
					Online:       l.Online,
					AffectedGrps: []string{l.Group},
					LessonDef:    &req.Lessons[li],
				})
			case "lesson-merge":
				affected := make([]string, 0, len(l.MultipleIDs))
				for _, m := range l.MultipleIDs {
					affected = append(affected, m.GroupID)
				}
				s.containers = append(s.containers, Container{
					LessonID:     l.Identifier,
					DistIdx:      distIdx,
					Length:       distribution,
					Instructor:   l.Instructor,
					Group:        "",
					Unit:         l.Unit,
					Online:       l.Online,
					AffectedGrps: affected,
					LessonDef:    &req.Lessons[li],
				})
			case "subgroup":
				affected := []string{l.Group}
				s.containers = append(s.containers, Container{
					LessonID:     l.Identifier,
					DistIdx:      distIdx,
					Length:       distribution,
					Group:        l.Group,
					Unit:         l.Unit,
					Online:       l.Online,
					AffectedGrps: affected,
					LessonDef:    &req.Lessons[li],
				})
			case "subgroup-lesson-merge":
				// Merged electives are one simultaneous session per day-slot that
				// occupies every affected group + every combo's instructor/room.
				affected := make([]string, 0, len(l.MultipleIDs))
				for _, m := range l.MultipleIDs {
					affected = append(affected, m.GroupID)
				}
				s.containers = append(s.containers, Container{
					LessonID:     l.Identifier,
					DistIdx:      distIdx,
					Length:       distribution,
					Group:        l.Group,
					Unit:         l.Unit,
					Online:       l.Online,
					AffectedGrps: affected,
					LessonDef:    &req.Lessons[li],
				})
			}

			// grows in lockstep with s.containers -- one entry per container, no other source of truth
			s.assignments = append(s.assignments, Assignment{DayIdx: -1, TimeIdx: -1, RoomIdx: nil})
		}
	}
}

func (s *LazySolver) buildEvalParams() {
	periodTimes := make([]string, len(s.blocks))
	for i, block := range s.blocks {
		periodTimes[i] = block.Start
	}

	s.evalParams = &evaluator.EvaluationParams{
		Days:        s.days,
		PeriodTimes: periodTimes,
		Lessons:     s.lessons,
		Rooms:       s.rooms,
		Groups:      s.groups,
		Instructors: s.instructors,
		Units:       s.units,
		Blocks:      make([]models.Block, 0),
	}
}

// withAssignedRooms returns a FRESH copy of base with RoomID filled in from
// roomIdxs, matched by position (RoomIdx[i] is the room for base[i], per the
// "in the order they are assigned" ordering on Assignment.RoomIdx), and the
// session's online flag stamped onto every entry (per the online-tagging
// mandate: root and every multiple_ids entry must agree).
//
// It deliberately does not mutate base in place: base is def.MultipleIDs,
// which is a pointer shared by every distIdx session of this same lesson --
// writing into it directly would leak one session's room assignment into
// every other session of the same lesson.
func withAssignedRooms(base []models.SubgroupID, roomIdxs []int, rooms []models.Room, online bool) []models.SubgroupID {
	out := make([]models.SubgroupID, len(base))
	copy(out, base) // shallow copy of the structs themselves (no pointers inside SubgroupID, so this is a real copy)

	for i := range out {
		out[i].Online = online
		if i < len(roomIdxs) {
			r := roomIdxs[i]
			if r >= 0 && r < len(rooms) {
				out[i].RoomID = rooms[r].ID
			}
		}
	}

	return out
}

func (s *LazySolver) formatResponse(runtime float64) *models.Response {
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
			Day:         day,
			Time:        timeStr,
			Room:        room,
			Unit:        container.Unit,
			Online:      container.Online,
			Blocks:      container.Length,
			Multiple:    def.Multiple,
			Type:        def.Type,
			Short:       def.Short,
			Title:       def.Title,
			DatabaseIDs: def.DatabaseIDs,
			TimetableID: def.TimetableID,
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
		Error:    false,
		Message:  "Lazy",
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