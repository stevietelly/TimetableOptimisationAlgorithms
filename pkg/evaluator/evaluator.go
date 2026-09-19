package evaluator

import (
	"fmt"
	"geliana-go/pkg/models"
	"math"
	"sort"
	"strconv"
	"strings"
)

// EvaluationResult holds the full scoring breakdown.
type EvaluationResult struct {
	HardScore         float64               `json:"hard_score"`
	PreferenceScore   float64               `json:"preference_score"`
	DistributionScore float64               `json:"distribution_score"`
	DefaultRoomScore  float64               `json:"default_room_score"`
	OverallScore      float64               `json:"overall_score"`
	TotalClashes      int                   `json:"total_clashes"`
	HiddenSessions    int                   `json:"hidden_sessions"`
	MissingSessions   int                   `json:"missing_sessions"`
	ExtraSessions     int                   `json:"extra_sessions"`
	Health            string                `json:"health"`
	Violations        []ConstraintViolation `json:"violations,omitempty"`
}

// ConstraintViolation records a single violation.
type ConstraintViolation struct {
	Type        string  `json:"type"`
	Description string  `json:"description"`
	Penalty     float64 `json:"penalty"`
}

// EvaluationParams provides the context needed to evaluate sessions.
type EvaluationParams struct {
	Days        []string
	PeriodTimes []string
	Lessons     []models.Lesson
	Rooms       []models.Room
	Groups      []models.Group
	Instructors []models.Instructor
	Units       []models.Unit
	Blocks      []models.Block
	Breaks      []models.Break
}

// Evaluate runs the full constraint evaluation on a set of sessions.
func Evaluate(sessions []models.Session, params *EvaluationParams) *EvaluationResult {
	res := &EvaluationResult{}

	if len(sessions) == 0 {
		res.HardScore = 0
		res.PreferenceScore = 0
		res.DistributionScore = 0
		res.DefaultRoomScore = 0
		res.OverallScore = 0
		res.Health = "low"
		return res
	}

	var periodTimes []string
	var days []string
	if params != nil {
		periodTimes = params.PeriodTimes
		days = params.Days
	}

	// 1. Hard constraints — clashes
	hardScore, totalClashes, clashViolations := evaluateClashes(sessions, periodTimes)
	res.HardScore = hardScore
	res.TotalClashes = totalClashes
	res.Violations = append(res.Violations, clashViolations...)

	// 2. Hidden sessions
	hiddenSessions := evaluateHiddenSessions(sessions, days, periodTimes)
	res.HiddenSessions = hiddenSessions
	if hiddenSessions > 0 {
		res.Violations = append(res.Violations, ConstraintViolation{
			Type:        "hidden_session",
			Description: fmt.Sprintf("%d sessions outside valid day/time grid", hiddenSessions),
			Penalty:     0,
		})
	}

	// 3. Preferences
	if params != nil {
		prefScore, prefViolations := evaluatePreferences(sessions, params)
		res.PreferenceScore = prefScore
		res.Violations = append(res.Violations, prefViolations...)
	} else {
		res.PreferenceScore = 100.0
	}

	// 4. Lesson distribution & irregularities
	if params != nil {
		distScore, missing, extra, distViolations := evaluateDistribution(sessions, params)
		res.DistributionScore = distScore
		res.MissingSessions = missing
		res.ExtraSessions = extra
		res.Violations = append(res.Violations, distViolations...)
	} else {
		res.DistributionScore = 100.0
	}

	// 5. Default rooms
	if params != nil {
		drScore, drViolations := evaluateDefaultRooms(sessions, params)
		res.DefaultRoomScore = drScore
		res.Violations = append(res.Violations, drViolations...)
	} else {
		res.DefaultRoomScore = 100.0
	}

	// 6. Overall
	res.OverallScore = calculateOverall(res)

	// 7. Health
	res.Health = calculateHealth(res, len(sessions))

	return res
}

// ---------------------------------------------------------------------------
// 0. Unique Key Association (mirrors TypeScript createUniqueSessionLessonKey)
// ---------------------------------------------------------------------------

// CreateUniqueSessionKey creates a content-based identifier for a session.
func CreateUniqueSessionKey(s models.Session) string {
	switch s.Type {
	case "regular":
		return fmt.Sprintf("rg@%s@%s@%s", s.Group, s.Unit, s.Instructor)
	case "lesson-merge":
		groupIDs := make([]string, len(s.MultipleIDs))
		for i, m := range s.MultipleIDs {
			groupIDs[i] = m.GroupID
		}
		inst := s.Instructor
		if inst == "" && len(s.MultipleIDs) > 0 {
			inst = s.MultipleIDs[0].InstID
		}
		return fmt.Sprintf("lm@%s@%s@%s", strings.Join(groupIDs, "-"), s.Unit, inst)
	case "subgroup-lesson-merge":
		type combo struct {
			key, groupID, unitID, instID string
		}
		combos := make([]combo, len(s.MultipleIDs))
		for i, m := range s.MultipleIDs {
			combos[i] = combo{
				key:     fmt.Sprintf("%s:%s:%s", m.GroupID, m.UnitID, m.InstID),
				groupID: m.GroupID,
				unitID:  m.UnitID,
				instID:  m.InstID,
			}
		}
		sort.Slice(combos, func(i, j int) bool { return combos[i].key < combos[j].key })
		groups := make([]string, len(combos))
		units := make([]string, len(combos))
		insts := make([]string, len(combos))
		for i, c := range combos {
			groups[i] = c.groupID
			units[i] = c.unitID
			insts[i] = c.instID
		}
		return fmt.Sprintf("sm@%s@%s@%s", strings.Join(groups, "-"), strings.Join(units, "-"), strings.Join(insts, "-"))
	default:
		// "subgroup"
		if len(s.MultipleIDs) > 0 {
			units := make([]string, len(s.MultipleIDs))
			insts := make([]string, len(s.MultipleIDs))
			for i, m := range s.MultipleIDs {
				units[i] = m.UnitID
				insts[i] = m.InstID
			}
			return fmt.Sprintf("sg@%s@%s@%s", s.Group, strings.Join(units, "-"), strings.Join(insts, "-"))
		}
		return fmt.Sprintf("rg@%s@%s@%s", s.Group, s.Unit, s.Instructor)
	}
}

// CreateUniqueLessonKey creates a content-based identifier for a lesson.
func CreateUniqueLessonKey(l models.Lesson) string {
	switch l.Type {
	case "regular":
		return fmt.Sprintf("rg@%s@%s@%s", l.Group, l.Unit, l.Instructor)
	case "lesson-merge":
		groupIDs := make([]string, len(l.MultipleIDs))
		for i, m := range l.MultipleIDs {
			groupIDs[i] = m.GroupID
		}
		inst := l.Instructor
		if inst == "" && len(l.MultipleIDs) > 0 {
			inst = l.MultipleIDs[0].InstID
		}
		return fmt.Sprintf("lm@%s@%s@%s", strings.Join(groupIDs, "-"), l.Unit, inst)
	case "subgroup-lesson-merge":
		type combo struct {
			key, groupID, unitID, instID string
		}
		combos := make([]combo, len(l.MultipleIDs))
		for i, m := range l.MultipleIDs {
			combos[i] = combo{
				key:     fmt.Sprintf("%s:%s:%s", m.GroupID, m.UnitID, m.InstID),
				groupID: m.GroupID,
				unitID:  m.UnitID,
				instID:  m.InstID,
			}
		}
		sort.Slice(combos, func(i, j int) bool { return combos[i].key < combos[j].key })
		groups := make([]string, len(combos))
		units := make([]string, len(combos))
		insts := make([]string, len(combos))
		for i, c := range combos {
			groups[i] = c.groupID
			units[i] = c.unitID
			insts[i] = c.instID
		}
		return fmt.Sprintf("sm@%s@%s@%s", strings.Join(groups, "-"), strings.Join(units, "-"), strings.Join(insts, "-"))
	default:
		// "subgroup"
		if len(l.MultipleIDs) > 0 {
			units := make([]string, len(l.MultipleIDs))
			insts := make([]string, len(l.MultipleIDs))
			for i, m := range l.MultipleIDs {
				units[i] = m.UnitID
				insts[i] = m.InstID
			}
			return fmt.Sprintf("sg@%s@%s@%s", l.Group, strings.Join(units, "-"), strings.Join(insts, "-"))
		}
		return fmt.Sprintf("rg@%s@%s@%s", l.Group, l.Unit, l.Instructor)
	}
}

// ---------------------------------------------------------------------------
// 1. Hard Constraints — Clashes
// ---------------------------------------------------------------------------

type sessionRange struct {
	identifier string
	startBlock int
	endBlock   int
}

func blocksOverlap(a, b sessionRange) bool {
	return a.startBlock < b.endBlock && b.startBlock < a.endBlock
}

func evaluateClashes(sessions []models.Session, periodTimes []string) (float64, int, []ConstraintViolation) {
	var violations []ConstraintViolation
	totalClashes := 0

	type bucketKey struct {
		day    string
		entity string
	}
	groupBuckets := make(map[bucketKey][]sessionRange)
	instrBuckets := make(map[bucketKey][]sessionRange)
	roomBuckets := make(map[bucketKey][]sessionRange)

	timeOrder := make(map[string]int, len(periodTimes))
	if len(periodTimes) > 0 {
		for i, t := range periodTimes {
			timeOrder[strings.ToLower(strings.TrimSpace(t))] = i
		}
	} else {
		allTimes := make([]string, 0, len(sessions))
		seen := make(map[string]bool)
		for _, s := range sessions {
			norm := strings.ToLower(strings.TrimSpace(s.Time))
			if !seen[norm] {
				seen[norm] = true
				allTimes = append(allTimes, s.Time)
			}
		}
		sortTimes(allTimes)
		for i, t := range allTimes {
			timeOrder[strings.ToLower(strings.TrimSpace(t))] = i
		}
	}

	missing := make([]string, 0)
	for _, s := range sessions {
		norm := strings.ToLower(strings.TrimSpace(s.Time))
		if _, ok := timeOrder[norm]; !ok {
			timeOrder[norm] = -1
			missing = append(missing, s.Time)
		}
	}
	if len(missing) > 0 {
		sortTimes(missing)
		for _, t := range missing {
			norm := strings.ToLower(strings.TrimSpace(t))
			timeOrder[norm] = len(timeOrder)
		}
	}

	for _, s := range sessions {
		normTime := strings.ToLower(strings.TrimSpace(s.Time))
		startBlock := timeOrder[normTime]
		blocks := s.Blocks
		if blocks < 1 {
			blocks = 1
		}
		endBlock := startBlock + blocks

		sr := sessionRange{s.Identifier, startBlock, endBlock}

		// 1. Group entities
		var groups []string
		if s.Multiple {
			if s.Type == "lesson-merge" || s.Type == "subgroup-lesson-merge" {
				for _, m := range s.MultipleIDs {
					if m.GroupID != "" {
						groups = append(groups, m.GroupID)
					}
				}
			} else {
				if s.Group != "" {
					groups = append(groups, s.Group)
				}
			}
		} else {
			if s.Group != "" {
				groups = append(groups, s.Group)
			}
		}
		if len(groups) == 0 && len(s.AffectedGroups) > 0 {
			groups = s.AffectedGroups
		}
		seenG := make(map[string]bool)
		for _, g := range groups {
			if g != "" && !seenG[g] {
				seenG[g] = true
				key := bucketKey{s.Day, g}
				groupBuckets[key] = append(groupBuckets[key], sr)
			}
		}

		// 2. Instructor entities
		var instrs []string
		if s.Multiple {
			if s.Type == "lesson-merge" {
				if s.Instructor != "" {
					instrs = append(instrs, s.Instructor)
				}
			} else {
				for _, m := range s.MultipleIDs {
					if m.InstID != "" {
						instrs = append(instrs, m.InstID)
					}
				}
			}
		} else {
			if s.Instructor != "" {
				instrs = append(instrs, s.Instructor)
			}
		}
		if len(instrs) == 0 && len(s.AffectedInstructors) > 0 {
			instrs = s.AffectedInstructors
		}
		seenI := make(map[string]bool)
		for _, inst := range instrs {
			if inst != "" && !seenI[inst] {
				seenI[inst] = true
				key := bucketKey{s.Day, inst}
				instrBuckets[key] = append(instrBuckets[key], sr)
			}
		}

		// 3. Room entities (skip online)
		var rooms []string
		if s.Multiple {
			if s.Type == "lesson-merge" {
				if !s.Online && s.Room != "" {
					rooms = append(rooms, strings.Split(s.Room, ",")...)
				}
			} else {
				for _, m := range s.MultipleIDs {
					if !m.Online && m.RoomID != "" {
						rooms = append(rooms, strings.Split(m.RoomID, ",")...)
					}
				}
			}
		} else {
			if !s.Online && s.Room != "" {
				rooms = append(rooms, strings.Split(s.Room, ",")...)
			}
		}
		seenR := make(map[string]bool)
		for _, r := range rooms {
			r = strings.TrimSpace(r)
			if r != "" && !seenR[r] {
				seenR[r] = true
				key := bucketKey{s.Day, r}
				roomBuckets[key] = append(roomBuckets[key], sr)
			}
		}
	}

	addClashViolations := func(buckets map[bucketKey][]sessionRange, clashType string) {
		for key, list := range buckets {
			for i := 0; i < len(list); i++ {
				for j := i + 1; j < len(list); j++ {
					if blocksOverlap(list[i], list[j]) {
						totalClashes++
						violations = append(violations, ConstraintViolation{
							Type:        clashType,
							Description: fmt.Sprintf("%s: %s and %s overlap at %s/%s", clashType, list[i].identifier, list[j].identifier, key.day, key.entity),
							Penalty:     1.0,
						})
					}
				}
			}
		}
	}

	addClashViolations(groupBuckets, "group_clash")
	addClashViolations(instrBuckets, "instructor_clash")
	addClashViolations(roomBuckets, "room_clash")

	n := len(sessions)
	if n == 0 {
		return 100.0, 0, violations
	}
	clashRatio := float64(totalClashes) / float64(n)
	score := math.Max(0, 100.0*(1.0-math.Min(1.0, clashRatio*2.0)))
	return score, totalClashes, violations
}

// sortTimes sorts time strings like "8:00am", "9:00am" in chronological order.
func sortTimes(times []string) {
	sort.Slice(times, func(i, j int) bool {
		return timeToMinutes(times[i]) < timeToMinutes(times[j])
	})
}

// ---------------------------------------------------------------------------
// 2. Hidden Sessions
// ---------------------------------------------------------------------------

func evaluateHiddenSessions(sessions []models.Session, days, periodTimes []string) int {
	if len(days) == 0 && len(periodTimes) == 0 {
		return 0
	}

	daySet := make(map[string]bool, len(days))
	for _, d := range days {
		daySet[strings.ToLower(strings.TrimSpace(d))] = true
	}
	timeSet := make(map[string]bool, len(periodTimes))
	for _, t := range periodTimes {
		timeSet[strings.ToLower(strings.TrimSpace(t))] = true
	}

	hidden := 0
	for _, s := range sessions {
		dayMatch := len(daySet) == 0 || daySet[strings.ToLower(strings.TrimSpace(s.Day))]
		timeMatch := len(timeSet) == 0 || timeSet[strings.ToLower(strings.TrimSpace(s.Time))]
		if !dayMatch || !timeMatch {
			hidden++
		}
	}
	return hidden
}

// ---------------------------------------------------------------------------
// 3. Preferences
// ---------------------------------------------------------------------------

func evaluatePreferences(sessions []models.Session, params *EvaluationParams) (float64, []ConstraintViolation) {
	var violations []ConstraintViolation
	totalChecks := 0
	violationCount := 0

	groupMap := make(map[string]models.Group, len(params.Groups))
	for _, g := range params.Groups {
		groupMap[g.ID] = g
	}
	unitMap := make(map[string]models.Unit, len(params.Units))
	for _, u := range params.Units {
		unitMap[u.ID] = u
	}
	instrMap := make(map[string]models.Instructor, len(params.Instructors))
	for _, i := range params.Instructors {
		instrMap[i.ID] = i
	}

	dayIndex := make(map[string]int, len(params.Days))
	for i, d := range params.Days {
		dayIndex[strings.ToLower(strings.TrimSpace(d))] = i
	}

	for _, s := range sessions {
		entities := []struct {
			prefs []models.Preference
			name  string
		}{}

		if !s.Multiple || s.Type == "regular" {
			if u, ok := unitMap[s.Unit]; ok && len(u.Preferences) > 0 {
				entities = append(entities, struct {
					prefs []models.Preference
					name  string
				}{u.Preferences, "unit:" + s.Unit})
			}
			var grps []string
			if s.Group != "" {
				grps = append(grps, s.Group)
			}
			if len(grps) == 0 && len(s.AffectedGroups) > 0 {
				grps = s.AffectedGroups
			}
			for _, gID := range grps {
				if g, ok := groupMap[gID]; ok && len(g.Preferences) > 0 {
					entities = append(entities, struct {
						prefs []models.Preference
						name  string
					}{g.Preferences, "group:" + gID})
				}
			}
			var instrs []string
			if s.Instructor != "" {
				instrs = append(instrs, s.Instructor)
			}
			if len(instrs) == 0 && len(s.AffectedInstructors) > 0 {
				instrs = s.AffectedInstructors
			}
			for _, iID := range instrs {
				if i, ok := instrMap[iID]; ok && len(i.Preferences) > 0 {
					entities = append(entities, struct {
						prefs []models.Preference
						name  string
					}{i.Preferences, "instructor:" + iID})
				}
			}
		} else if s.Type == "subgroup" {
			if g, ok := groupMap[s.Group]; ok && len(g.Preferences) > 0 {
				entities = append(entities, struct {
					prefs []models.Preference
					name  string
				}{g.Preferences, "group:" + s.Group})
			}
			seenU := make(map[string]bool)
			seenI := make(map[string]bool)
			for _, mid := range s.MultipleIDs {
				if mid.InstID != "" && !seenI[mid.InstID] {
					seenI[mid.InstID] = true
					if inst, ok := instrMap[mid.InstID]; ok && len(inst.Preferences) > 0 {
						entities = append(entities, struct {
							prefs []models.Preference
							name  string
						}{inst.Preferences, "instructor:" + mid.InstID})
					}
				}
				if mid.UnitID != "" && !seenU[mid.UnitID] {
					seenU[mid.UnitID] = true
					if u, ok := unitMap[mid.UnitID]; ok && len(u.Preferences) > 0 {
						entities = append(entities, struct {
							prefs []models.Preference
							name  string
						}{u.Preferences, "unit:" + mid.UnitID})
					}
				}
			}
		} else if s.Type == "subgroup-lesson-merge" {
			seenG := make(map[string]bool)
			seenU := make(map[string]bool)
			seenI := make(map[string]bool)
			for _, mid := range s.MultipleIDs {
				if mid.GroupID != "" && !seenG[mid.GroupID] {
					seenG[mid.GroupID] = true
					if g, ok := groupMap[mid.GroupID]; ok && len(g.Preferences) > 0 {
						entities = append(entities, struct {
							prefs []models.Preference
							name  string
						}{g.Preferences, "group:" + mid.GroupID})
					}
				}
				if mid.InstID != "" && !seenI[mid.InstID] {
					seenI[mid.InstID] = true
					if inst, ok := instrMap[mid.InstID]; ok && len(inst.Preferences) > 0 {
						entities = append(entities, struct {
							prefs []models.Preference
							name  string
						}{inst.Preferences, "instructor:" + mid.InstID})
					}
				}
				if mid.UnitID != "" && !seenU[mid.UnitID] {
					seenU[mid.UnitID] = true
					if u, ok := unitMap[mid.UnitID]; ok && len(u.Preferences) > 0 {
						entities = append(entities, struct {
							prefs []models.Preference
							name  string
						}{u.Preferences, "unit:" + mid.UnitID})
					}
				}
			}
		} else { // lesson-merge
			seenG := make(map[string]bool)
			for _, mid := range s.MultipleIDs {
				if mid.GroupID != "" && !seenG[mid.GroupID] {
					seenG[mid.GroupID] = true
					if g, ok := groupMap[mid.GroupID]; ok && len(g.Preferences) > 0 {
						entities = append(entities, struct {
							prefs []models.Preference
							name  string
						}{g.Preferences, "group:" + mid.GroupID})
					}
				}
			}
			if len(seenG) == 0 {
				for _, gID := range s.AffectedGroups {
					if gID != "" && !seenG[gID] {
						seenG[gID] = true
						if g, ok := groupMap[gID]; ok && len(g.Preferences) > 0 {
							entities = append(entities, struct {
								prefs []models.Preference
								name  string
							}{g.Preferences, "group:" + gID})
						}
					}
				}
			}
			var instrs []string
			if s.Instructor != "" {
				instrs = append(instrs, s.Instructor)
			}
			if len(instrs) == 0 && len(s.AffectedInstructors) > 0 {
				instrs = s.AffectedInstructors
			}
			for _, iID := range instrs {
				if inst, ok := instrMap[iID]; ok && len(inst.Preferences) > 0 {
					entities = append(entities, struct {
						prefs []models.Preference
						name  string
					}{inst.Preferences, "instructor:" + iID})
				}
			}
			if u, ok := unitMap[s.Unit]; ok && len(u.Preferences) > 0 {
				entities = append(entities, struct {
					prefs []models.Preference
					name  string
				}{u.Preferences, "unit:" + s.Unit})
			}
		}

		for _, ent := range entities {
			for _, pref := range ent.prefs {
				totalChecks++
				if isPreferenceViolated(s, pref, dayIndex, params) {
					violationCount++
					violations = append(violations, ConstraintViolation{
						Type:        "preference",
						Description: fmt.Sprintf("%s: %s %s %s violated for %s", s.Identifier, pref.Type, pref.Target.Kind, pref.Target.Value, ent.name),
						Penalty:     1.0,
					})
				}
			}
		}
	}

	if totalChecks == 0 {
		return 100.0, violations
	}
	score := float64(totalChecks-violationCount) / float64(totalChecks) * 100.0
	return score, violations
}

func matchRoom(sessionRooms string, targetRoom string) bool {
	target := strings.ToLower(strings.TrimSpace(targetRoom))
	for _, r := range strings.Split(sessionRooms, ",") {
		if strings.ToLower(strings.TrimSpace(r)) == target {
			return true
		}
	}
	return false
}

func isPreferenceViolated(s models.Session, pref models.Preference, dayIndex map[string]int, params *EvaluationParams) bool {
	kind := pref.Target.Kind
	value := pref.Target.Value

	dayLwr := strings.ToLower(strings.TrimSpace(s.Day))
	valLwr := strings.ToLower(strings.TrimSpace(value))

	switch pref.Type {
	case "ONLY":
		switch kind {
		case "DAY":
			return valLwr != dayLwr
		case "TIME":
			return !compareTimeEqual(s.Time, value)
		case "ROOM":
			return !matchRoom(s.Room, value)
		}

	case "EXCEPT":
		switch kind {
		case "DAY":
			return valLwr == dayLwr
		case "TIME":
			return compareTimeEqual(s.Time, value)
		case "ROOM":
			return matchRoom(s.Room, value)
		}

	case "BEFORE":
		switch kind {
		case "DAY":
			sIdx, ok1 := dayIndex[dayLwr]
			tIdx, ok2 := dayIndex[valLwr]
			if ok1 && ok2 {
				return sIdx >= tIdx
			}
		case "TIME":
			return !compareTimeBefore(s.Time, value)
		case "PERIOD":
			return isSessionNotBeforePeriod(s, value, params)
		case "BREAK":
			return isSessionNotBeforeBreak(s, value, params)
		}

	case "AFTER":
		switch kind {
		case "DAY":
			sIdx, ok1 := dayIndex[dayLwr]
			tIdx, ok2 := dayIndex[valLwr]
			if ok1 && ok2 {
				return sIdx <= tIdx
			}
		case "TIME":
			return !compareTimeAfter(s.Time, value)
		case "PERIOD":
			return isSessionNotAfterPeriod(s, value, params)
		case "BREAK":
			return isSessionNotAfterBreak(s, value, params)
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// 4. Lesson Distribution & Session Irregularities
// ---------------------------------------------------------------------------

func evaluateDistribution(sessions []models.Session, params *EvaluationParams) (float64, int, int, []ConstraintViolation) {
	var violations []ConstraintViolation

	// Build map from lesson unique key to lesson
	lessonKeyMap := make(map[string]models.Lesson, len(params.Lessons))
	for _, l := range params.Lessons {
		key := CreateUniqueLessonKey(l)
		lessonKeyMap[key] = l
	}

	// Map sessions to lessons: match by unique key first, fallback to identifier
	lessonSessions := make(map[string][]models.Session)
	for _, l := range params.Lessons {
		lKey := CreateUniqueLessonKey(l)
		var matched []models.Session
		for _, s := range sessions {
			sKey := CreateUniqueSessionKey(s)
			if sKey == lKey {
				matched = append(matched, s)
			} else if l.Identifier != "" && (s.Identifier == l.Identifier || strings.HasPrefix(s.Identifier, l.Identifier+"-")) {
				matched = append(matched, s)
			}
		}
		lessonSessions[l.Identifier] = matched
	}

	timeIdx := make(map[string]int, len(params.PeriodTimes))
	for i, t := range params.PeriodTimes {
		timeIdx[strings.ToLower(strings.TrimSpace(t))] = i
	}

	wellDistributed := 0
	totalLessons := 0
	missingSessions := 0
	extraSessions := 0

	for _, lesson := range params.Lessons {
		sessList := lessonSessions[lesson.Identifier]

		// Session count irregularities
		actualCount := 0
		for _, s := range sessList {
			blocks := s.Blocks
			if blocks < 1 {
				blocks = 1
			}
			actualCount += blocks
		}

		if lesson.TotalLessons > 0 {
			if actualCount < lesson.TotalLessons {
				missingSessions += lesson.TotalLessons - actualCount
			} else if actualCount > lesson.TotalLessons {
				extraSessions += actualCount - lesson.TotalLessons
			}
		}

		desiredTotal := 0
		var desired []int
		for _, c := range lesson.Distribution {
			if c > 0 {
				desiredTotal += c
				desired = append(desired, c)
			}
		}
		if desiredTotal == 0 {
			continue
		}

		totalLessons++

		// Reconstruct contiguous runs per day (matching TypeScript EvaluateLessonDistribution)
		daySlots := make(map[string][]int)
		offGrid := 0
		for _, s := range sessList {
			normTime := strings.ToLower(strings.TrimSpace(s.Time))
			ti, ok := timeIdx[normTime]
			if !ok {
				offGrid++
				continue
			}
			dayKey := strings.ToLower(strings.TrimSpace(s.Day))
			blocks := s.Blocks
			if blocks < 1 {
				blocks = 1
			}
			for b := 0; b < blocks; b++ {
				daySlots[dayKey] = append(daySlots[dayKey], ti+b)
			}
		}

		var actual []int
		for _, idxs := range daySlots {
			sort.Ints(idxs)
			// Remove duplicates in case of overlaps
			uniqueIdxs := make([]int, 0, len(idxs))
			for i, val := range idxs {
				if i == 0 || val != idxs[i-1] {
					uniqueIdxs = append(uniqueIdxs, val)
				}
			}

			run := 1
			for i := 1; i <= len(uniqueIdxs); i++ {
				if i < len(uniqueIdxs) && uniqueIdxs[i] == uniqueIdxs[i-1]+1 {
					run++
				} else {
					actual = append(actual, run)
					run = 1
				}
			}
		}
		for i := 0; i < offGrid; i++ {
			actual = append(actual, 1)
		}
		sort.Slice(actual, func(i, j int) bool { return actual[i] > actual[j] })
		sort.Slice(desired, func(i, j int) bool { return desired[i] > desired[j] })

		actualTotal := 0
		for _, a := range actual {
			actualTotal += a
		}

		fit := calculateFit(desired, desiredTotal, actual, actualTotal)
		if fit >= 0.8 {
			wellDistributed++
		} else {
			violations = append(violations, ConstraintViolation{
				Type:        "distribution",
				Description: fmt.Sprintf("lesson %s: fit=%.2f (desired=%v, actual=%v)", lesson.Identifier, fit, desired, actual),
				Penalty:     1.0 - fit,
			})
		}
	}

	if totalLessons == 0 {
		return 100.0, missingSessions, extraSessions, violations
	}
	score := float64(wellDistributed) / float64(totalLessons) * 100.0
	return score, missingSessions, extraSessions, violations
}

func calculateFit(desiredBlocks []int, desiredTotal int, actualBlocks []int, actualTotal int) float64 {
	if actualTotal == 0 {
		return 0.0
	}

	var fit float64
	if desiredTotal == actualTotal {
		if len(desiredBlocks) == len(actualBlocks) {
			match := true
			for i := range desiredBlocks {
				if desiredBlocks[i] != actualBlocks[i] {
					match = false
					break
				}
			}
			if match {
				return 1.0
			}
		}

		maxBlocks := len(desiredBlocks)
		if len(actualBlocks) > maxBlocks {
			maxBlocks = len(actualBlocks)
		}
		matchScore := 0.0
		for i := 0; i < maxBlocks; i++ {
			d := 0
			if i < len(desiredBlocks) {
				d = desiredBlocks[i]
			}
			a := 0
			if i < len(actualBlocks) {
				a = actualBlocks[i]
			}
			if d > 0 {
				matchScore += 1.0 - math.Abs(float64(d-a))/float64(d)
			}
		}
		fit = matchScore / float64(max(1, len(desiredBlocks)))
	} else {
		countMatch := math.Max(0, 1.0-math.Abs(float64(actualTotal-desiredTotal))/float64(max(1, desiredTotal)))
		fit = countMatch * 0.5
	}

	return math.Min(1.0, math.Max(0.0, math.Round(fit*100)/100))
}

// ---------------------------------------------------------------------------
// 5. Default Room Satisfaction
// ---------------------------------------------------------------------------

func evaluateDefaultRooms(sessions []models.Session, params *EvaluationParams) (float64, []ConstraintViolation) {
	var violations []ConstraintViolation

	lessonMap := make(map[string]models.Lesson, len(params.Lessons))
	for _, l := range params.Lessons {
		key := l.Group + "|" + l.Unit
		lessonMap[key] = l
	}

	groupMap := make(map[string]models.Group, len(params.Groups))
	for _, g := range params.Groups {
		groupMap[g.ID] = g
	}

	unitMap := make(map[string]models.Unit, len(params.Units))
	for _, u := range params.Units {
		unitMap[u.ID] = u
	}

	satisfied := 0
	totalWithPref := 0

	for _, s := range sessions {
		if s.Online {
			continue
		}

		var roomsToCheck []string
		if !s.Multiple || s.Type == "regular" || s.Type == "lesson-merge" {
			if s.Room != "" {
				roomsToCheck = append(roomsToCheck, s.Room)
			}
		} else {
			for _, mid := range s.MultipleIDs {
				if !mid.Online && mid.RoomID != "" {
					roomsToCheck = append(roomsToCheck, mid.RoomID)
				}
			}
		}

		if len(roomsToCheck) == 0 {
			continue
		}

		preferred := ""

		// 1. Lesson's default_room
		lessonKey := s.Group + "|" + s.Unit
		if l, ok := lessonMap[lessonKey]; ok && l.DefaultRoom != "" {
			preferred = l.DefaultRoom
		}

		// 2. Unit's default_room
		if preferred == "" {
			if u, ok := unitMap[s.Unit]; ok && u.DefaultRoom != "" {
				preferred = u.DefaultRoom
			}
		}

		// 3. Group's default_room
		if preferred == "" {
			if g, ok := groupMap[s.Group]; ok && g.DefaultRoom != "" {
				preferred = g.DefaultRoom
			}
		}

		if preferred == "" {
			continue
		}

		totalWithPref++

		matched := false
		for _, rawRoom := range roomsToCheck {
			for _, r := range strings.Split(rawRoom, ",") {
				if strings.TrimSpace(r) == preferred {
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}

		if matched {
			satisfied++
		} else {
			violations = append(violations, ConstraintViolation{
				Type:        "default_room",
				Description: fmt.Sprintf("%s: expected room %q, got %q", s.Identifier, preferred, s.Room),
				Penalty:     1.0,
			})
		}
	}

	if totalWithPref == 0 {
		return 100.0, violations
	}
	score := float64(satisfied) / float64(totalWithPref) * 100.0
	return score, violations
}

// ---------------------------------------------------------------------------
// 6. Helpers
// ---------------------------------------------------------------------------

func calculateOverall(res *EvaluationResult) float64 {
	return math.Round((res.HardScore*0.60+res.PreferenceScore*0.20+
		res.DistributionScore*0.10+res.DefaultRoomScore*0.10)*10) / 10
}

func calculateHealth(res *EvaluationResult, totalSessions int) string {
	sessionIrregularities := res.HiddenSessions + res.MissingSessions + res.ExtraSessions
	if sessionIrregularities > 10 || (totalSessions > 0 && float64(sessionIrregularities) > float64(totalSessions)*0.2) {
		return "low"
	}
	health := "low"
	if res.OverallScore >= 85 {
		health = "high"
	} else if res.OverallScore >= 60 {
		health = "medium"
	}
	if sessionIrregularities > 0 && health == "high" {
		health = "medium"
	}
	return health
}

func timeToMinutes(t string) int {
	t = strings.ToLower(strings.TrimSpace(t))

	meridiem := ""
	if strings.HasSuffix(t, "am") {
		meridiem = "am"
		t = strings.TrimSuffix(t, "am")
	} else if strings.HasSuffix(t, "pm") {
		meridiem = "pm"
		t = strings.TrimSuffix(t, "pm")
	}

	parts := strings.Split(t, ":")
	if len(parts) == 0 {
		return 0
	}
	h, _ := strconv.Atoi(parts[0])
	m := 0
	if len(parts) > 1 {
		m, _ = strconv.Atoi(parts[1])
	}

	if meridiem == "pm" && h != 12 {
		h += 12
	} else if meridiem == "am" && h == 12 {
		h = 0
	}

	return h*60 + m
}

func compareTimeEqual(a, b string) bool {
	return timeToMinutes(a) == timeToMinutes(b)
}

func compareTimeBefore(a, b string) bool {
	return timeToMinutes(a) < timeToMinutes(b)
}

func compareTimeAfter(a, b string) bool {
	return timeToMinutes(a) > timeToMinutes(b)
}

func sessionEndMinutes(s models.Session) int {
	return timeToMinutes(s.Time) + 40
}

func isSessionNotBeforePeriod(s models.Session, periodTitle string, params *EvaluationParams) bool {
	for _, b := range params.Blocks {
		if b.Title == periodTitle {
			periodStart := timeToMinutes(b.Start)
			endTime := sessionEndMinutes(s)
			return endTime >= periodStart
		}
	}
	return false
}

func isSessionNotBeforeBreak(s models.Session, breakTitle string, params *EvaluationParams) bool {
	for _, b := range params.Breaks {
		if b.Title == breakTitle {
			breakStart := timeToMinutes(b.StartTime)
			endTime := sessionEndMinutes(s)
			return endTime >= breakStart
		}
	}
	return false
}

func isSessionNotAfterPeriod(s models.Session, periodTitle string, params *EvaluationParams) bool {
	for _, b := range params.Blocks {
		if b.Title == periodTitle {
			periodStart := timeToMinutes(b.Start)
			periodDuration := b.Duration.Hours*60 + b.Duration.Minutes + b.Duration.Duration
			if periodDuration <= 0 {
				periodDuration = 40
			}
			sessionStart := timeToMinutes(s.Time)
			return sessionStart <= (periodStart + periodDuration)
		}
	}
	return false
}

func isSessionNotAfterBreak(s models.Session, breakTitle string, params *EvaluationParams) bool {
	for _, b := range params.Breaks {
		if b.Title == breakTitle {
			breakStart := timeToMinutes(b.StartTime)
			breakDuration := b.Duration.Hours*60 + b.Duration.Minutes + b.Duration.Duration
			if breakDuration <= 0 {
				breakDuration = 15
			}
			sessionStart := timeToMinutes(s.Time)
			return sessionStart <= (breakStart + breakDuration)
		}
	}
	return false
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
