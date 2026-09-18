package solver

import (
	"geliana-go/pkg/models"
)

type Assignment struct {
	DayIdx  int
	TimeIdx int
	RoomIdx []int // empty for online; one entry per room needed, in MultipleIDs order
}

type Container struct {
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

type occupancyDictonary struct {
	group     map[string]map[occKey]bool
	instr     map[string]map[occKey]bool
	room      map[int]map[occKey]bool
	lessonDay map[string]map[int]bool
}

func newOccupancyDictonary() *occupancyDictonary {
	return &occupancyDictonary{
		group:     make(map[string]map[occKey]bool),
		instr:     make(map[string]map[occKey]bool),
		room:      make(map[int]map[occKey]bool),
		lessonDay: make(map[string]map[int]bool),
	}
}

// lessonDayKey returns the key used in the lessonDay map for a container.
// Keyed by LessonID so the map tracks every session of a lesson together:
// once any slot of a lesson occupies a day, no other slot of that lesson
// may use the same day. (Keying per distribution slot would give every
// container a private entry that is always empty when checked, reducing
// lessonDayUsed to dead code.) Undo stays exact because the ban means two
// live placements never share a key+day, so removing the undone placement's
// day cannot strand a sibling entry.
func lessonDayKey(item *Container) string {
	return item.LessonID
}

// instructorIDs returns every instructor this container occupies, regardless
// of lesson type -- one shared place both add/remove/conflictCount can use,
// so the three never drift out of sync with each other again.
func instructorIDs(item *Container) []string {
	switch item.LessonDef.Type {
	case "subgroup", "subgroup-lesson-merge":
		ids := make([]string, 0, len(item.LessonDef.MultipleIDs))
		for _, m := range item.LessonDef.MultipleIDs {
			ids = append(ids, m.InstID)
		}
		return ids
	default: // "lesson-merge", "regular"
		if item.Instructor == "" {
			return nil
		}
		return []string{item.Instructor}
	}
}

func (o *occupancyDictonary) add(item *Container, ass Assignment) {
	for b := ass.TimeIdx; b < ass.TimeIdx+item.Length; b++ {
		k := occKey{ass.DayIdx, b}

		for _, grp := range item.AffectedGrps {
			if grp == "" {
				continue
			}
			if o.group[grp] == nil {
				o.group[grp] = make(map[occKey]bool)
			}
			o.group[grp][k] = true
		}

		for _, r := range ass.RoomIdx {
			if r == -1 {
				continue
			}
			if o.room[r] == nil {
				o.room[r] = make(map[occKey]bool)
			}
			o.room[r][k] = true
		}

		for _, inst := range instructorIDs(item) {
			if o.instr[inst] == nil {
				o.instr[inst] = make(map[occKey]bool)
			}
			o.instr[inst][k] = true
		}
	}

	if item.LessonID != "" {
		key := lessonDayKey(item)
		if o.lessonDay[key] == nil {
			o.lessonDay[key] = make(map[int]bool)
		}
		o.lessonDay[key][ass.DayIdx] = true
	}
}

func (o *occupancyDictonary) remove(item *Container, ass Assignment) {
	for b := ass.TimeIdx; b < ass.TimeIdx+item.Length; b++ {
		k := occKey{ass.DayIdx, b}

		for _, grp := range item.AffectedGrps {
			if grp != "" && o.group[grp] != nil {
				delete(o.group[grp], k)
			}
		}

		for _, r := range ass.RoomIdx {
			if r != -1 && o.room[r] != nil {
				delete(o.room[r], k)
			}
		}

		for _, inst := range instructorIDs(item) {
			if o.instr[inst] != nil {
				delete(o.instr[inst], k)
			}
		}
	}

	if item.LessonID != "" {
		key := lessonDayKey(item)
		if o.lessonDay[key] != nil {
			delete(o.lessonDay[key], ass.DayIdx)
		}
	}
}

func (o *occupancyDictonary) lessonDayUsed(item *Container, dayIdx int) bool {
	return o.lessonDay[lessonDayKey(item)][dayIdx]
}

// conflictCount checks a candidate placement against everything already
// committed. roomIdxs is the FULL set of rooms this placement would need --
// pass every candidate room at once so a clash on any one of them is caught.
func (o *occupancyDictonary) conflictCount(item *Container, dayIdx, blockIdx int, roomIdxs []int) int {
	penalty := 0
	for b := blockIdx; b < blockIdx+item.Length; b++ {
		k := occKey{dayIdx, b}

		for _, grp := range item.AffectedGrps {
			if grp != "" && o.group[grp] != nil && o.group[grp][k] {
				penalty++
			}
		}

		for _, r := range roomIdxs {
			if r != -1 && o.room[r] != nil && o.room[r][k] {
				penalty++
			}
		}

		for _, inst := range instructorIDs(item) {
			if o.instr[inst] != nil && o.instr[inst][k] {
				penalty++
			}
		}
	}
	return penalty
}