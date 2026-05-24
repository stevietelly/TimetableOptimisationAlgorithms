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
	HardScore       float64              `json:"hard_score"`
	PreferenceScore float64              `json:"preference_score"`
	DistributionScore float64            `json:"distribution_score"`
	DefaultRoomScore float64             `json:"default_room_score"`
	OverallScore    float64              `json:"overall_score"`
	TotalClashes    int                  `json:"total_clashes"`
	HiddenSessions  int                  `json:"hidden_sessions"`
	Health          string               `json:"health"`
	Violations      []ConstraintViolation `json:"violations,omitempty"`
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

	// 1. Hard constraints — clashes
	hardScore, totalClashes, clashViolations := evaluateClashes(sessions)
	res.HardScore = hardScore
	res.TotalClashes = totalClashes
	res.Violations = append(res.Violations, clashViolations...)

	// 2. Hidden sessions
	hiddenSessions := evaluateHiddenSessions(sessions, params.Days, params.PeriodTimes)
	res.HiddenSessions = hiddenSessions
	if hiddenSessions > 0 {
		res.Violations = append(res.Violations, ConstraintViolation{
			Type:        "hidden_session",
			Description: fmt.Sprintf("%d sessions outside valid day/time grid", hiddenSessions),
			Penalty:     0,
		})
	}

	// 3. Preferences
	prefScore, prefViolations := evaluatePreferences(sessions, params)
	res.PreferenceScore = prefScore
	res.Violations = append(res.Violations, prefViolations...)

	// 4. Lesson distribution
	distScore, distViolations := evaluateDistribution(sessions, params)
	res.DistributionScore = distScore
	res.Violations = append(res.Violations, distViolations...)

	// 5. Default rooms
	drScore, drViolations := evaluateDefaultRooms(sessions, params)
	res.DefaultRoomScore = drScore
	res.Violations = append(res.Violations, drViolations...)

	// 6. Overall
	res.OverallScore = calculateOverall(res)

	// 7. Health
	res.Health = calculateHealth(res, len(sessions))

	return res
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

func evaluateClashes(sessions []models.Session) (float64, int, []ConstraintViolation) {
	var violations []ConstraintViolation
	totalClashes := 0

	// Group by day + entity, then check block-range overlap
	type bucketKey struct {
		day    string
		entity string
	}
	groupBuckets := make(map[bucketKey][]sessionRange)
	instrBuckets := make(map[bucketKey][]sessionRange)
	roomBuckets := make(map[bucketKey][]sessionRange)

	// Build a quick time→block-index map from the first session's context.
	// Block indices are inferred from the time string order.
	// We use an ephemeral index: the evaluator doesn't have Blocks here directly,
	// so we derive relative ordering from the time strings.
	// Since we need actual block indices for overlap, we accept that sessions
	// with Blocks==1 have single-point ranges and don't overlap with same-time peers.
	// For Blocks>1, we mark the range starting at idx and ending at idx+Blocks.
	// We compute a session's start block by looking at the time string offset.
	// All sessions have a Time string; we use its ordinal position in sort order.
	// For simplicity, we sort all unique time strings and use that as the index.

	timeOrder := make(map[string]int)
	allTimes := make([]string, 0, len(sessions))
	seen := make(map[string]bool)
	for _, s := range sessions {
		if !seen[s.Time] {
			seen[s.Time] = true
			allTimes = append(allTimes, s.Time)
		}
	}
	sortTimes(allTimes)
	for i, t := range allTimes {
		timeOrder[t] = i
	}

	for _, s := range sessions {
		startBlock := timeOrder[s.Time]
		blocks := s.Blocks
		if blocks < 1 {
			blocks = 1
		}
		endBlock := startBlock + blocks

		sr := sessionRange{s.Identifier, startBlock, endBlock}

		if s.Group != "" {
			key := bucketKey{s.Day, s.Group}
			groupBuckets[key] = append(groupBuckets[key], sr)
		}
		if s.Instructor != "" {
			key := bucketKey{s.Day, s.Instructor}
			instrBuckets[key] = append(instrBuckets[key], sr)
		}
		if !s.Online && s.Room != "" {
			rooms := strings.Split(s.Room, ",")
			for _, room := range rooms {
				room = strings.TrimSpace(room)
				if room == "" {
					continue
				}
				key := bucketKey{s.Day, room}
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
	daySet := make(map[string]bool, len(days))
	for _, d := range days {
		daySet[d] = true
	}
	timeSet := make(map[string]bool, len(periodTimes))
	for _, t := range periodTimes {
		timeSet[t] = true
	}

	hidden := 0
	for _, s := range sessions {
		if !daySet[s.Day] || !timeSet[s.Time] {
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
		dayIndex[d] = i
	}

	for _, s := range sessions {
		entities := []struct {
			prefs []models.Preference
			name  string
		}{}

		if g, ok := groupMap[s.Group]; ok {
			entities = append(entities, struct {
				prefs []models.Preference
				name  string
			}{g.Preferences, "group:" + s.Group})
		}
		if u, ok := unitMap[s.Unit]; ok {
			entities = append(entities, struct {
				prefs []models.Preference
				name  string
			}{u.Preferences, "unit:" + s.Unit})
		}
		if i, ok := instrMap[s.Instructor]; ok {
			entities = append(entities, struct {
				prefs []models.Preference
				name  string
			}{i.Preferences, "instructor:" + s.Instructor})
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

func isPreferenceViolated(s models.Session, pref models.Preference, dayIndex map[string]int, params *EvaluationParams) bool {
	kind := pref.Target.Kind
	value := pref.Target.Value

	dayLwr := strings.ToLower(s.Day)

	switch pref.Type {
	case "ONLY":
		switch kind {
		case "DAY":
			return strings.ToLower(value) != dayLwr
		case "TIME":
			return !compareTimeEqual(s.Time, value)
		case "ROOM":
			return !strings.Contains(strings.ToLower(s.Room), strings.ToLower(value))
		}

	case "EXCEPT":
		switch kind {
		case "DAY":
			return strings.ToLower(value) == dayLwr
		case "TIME":
			return compareTimeEqual(s.Time, value)
		case "ROOM":
			return strings.Contains(strings.ToLower(s.Room), strings.ToLower(value))
		}

	case "BEFORE":
		switch kind {
		case "DAY":
			sIdx, ok1 := dayIndex[s.Day]
			tIdx, ok2 := dayIndex[value]
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
			sIdx, ok1 := dayIndex[s.Day]
			tIdx, ok2 := dayIndex[value]
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
// 4. Lesson Distribution
// ---------------------------------------------------------------------------

func evaluateDistribution(sessions []models.Session, params *EvaluationParams) (float64, []ConstraintViolation) {
	var violations []ConstraintViolation

	lessonMap := make(map[string]models.Lesson, len(params.Lessons))
	for _, l := range params.Lessons {
		lessonMap[l.Identifier] = l
	}

	// Count actual sessions per (lessonID, distIdx)
	type sessKey struct {
		lessonID string
		distIdx  int
	}
	actualCounts := make(map[sessKey]int)

	for _, s := range sessions {
		lessonID, distIdx := parseSessionIdentifier(s.Identifier)
		if lessonID == "" {
			continue
		}
		key := sessKey{lessonID, distIdx}
		count := s.Blocks
		if count < 1 {
			count = 1
		}
		actualCounts[key] += count
	}

	// Group by lesson
	type lessonDist struct {
		lessonID     string
		desiredTotal int
		desired      []int
	}
	lessonDists := make(map[string]*lessonDist)

	for key := range actualCounts {
		lesson, ok := lessonMap[key.lessonID]
		if !ok {
			continue
		}
		if _, exists := lessonDists[key.lessonID]; !exists {
			desiredTotal := 0
			for _, c := range lesson.Distribution {
				desiredTotal += c
			}
			// Only consider lessons that have a meaningful distribution
			if desiredTotal == 0 {
				continue
			}
			lessonDists[key.lessonID] = &lessonDist{
				lessonID:     key.lessonID,
				desiredTotal: desiredTotal,
				desired:      lesson.Distribution,
			}
		}
	}

	wellDistributed := 0
	totalLessons := len(lessonDists)

	for _, ld := range lessonDists {
		actualTotal := 0
		actualBlocks := make([]int, len(ld.desired))
		for i := range ld.desired {
			key := sessKey{ld.lessonID, i}
			actualBlocks[i] = actualCounts[key]
			actualTotal += actualBlocks[i]
		}

		fit := calculateFit(ld.desired, ld.desiredTotal, actualBlocks, actualTotal)
		if fit >= 0.8 {
			wellDistributed++
		} else {
			violations = append(violations, ConstraintViolation{
				Type:        "distribution",
				Description: fmt.Sprintf("lesson %s: fit=%.2f (desired=%v, actual=%v)", ld.lessonID, fit, ld.desired, actualBlocks),
				Penalty:     1.0 - fit,
			})
		}
	}

	if totalLessons == 0 {
		return 100.0, violations
	}
	score := float64(wellDistributed) / float64(totalLessons) * 100.0
	return score, violations
}

func calculateFit(desired []int, desiredTotal int, actualBlocks []int, actualTotal int) float64 {
	if actualTotal == 0 {
		return 0.0
	}

	if desiredTotal == actualTotal {
		desiredNonZero := 0
		for _, c := range desired {
			if c > 0 {
				desiredNonZero++
			}
		}
		actualNonZero := 0
		for _, c := range actualBlocks {
			if c > 0 {
				actualNonZero++
			}
		}

		if desiredNonZero == actualNonZero {
			matchCount := 0.0
			for i := range desired {
				if desired[i] > 0 && desired[i] == actualBlocks[i] {
					matchCount++
				}
			}
			if matchCount == float64(desiredNonZero) {
				return 1.0
			}
		}

		// Partial match per block
		matchScore := 0.0
		for i := range desired {
			if desired[i] > 0 {
				diff := math.Abs(float64(desired[i] - actualBlocks[i]))
				matchScore += 1.0 - diff/float64(desired[i])
			}
		}
		return matchScore / float64(max(1, desiredNonZero))
	}

	// Different totals
	countMatch := math.Max(0, 1.0-math.Abs(float64(actualTotal-desiredTotal))/float64(max(1, desiredTotal)))
	return countMatch * 0.5
}

// ---------------------------------------------------------------------------
// 5. Default Room Satisfaction
// ---------------------------------------------------------------------------

func evaluateDefaultRooms(sessions []models.Session, params *EvaluationParams) (float64, []ConstraintViolation) {
	var violations []ConstraintViolation

	lessonByGroupUnit := make(map[string]models.Lesson)
	for _, l := range params.Lessons {
		key := l.Group + "|" + l.Unit
		lessonByGroupUnit[key] = l
	}

	satisfied := 0
	totalWithPref := 0

	for _, s := range sessions {
		if s.Online {
			continue
		}

		preferred := ""

		// Check lesson default_room (matched by group+unit)
		lessonKey := s.Group + "|" + s.Unit
		if l, ok := lessonByGroupUnit[lessonKey]; ok && l.DefaultRoom != "" {
			preferred = l.DefaultRoom
		}

		if preferred == "" {
			continue
		}

		totalWithPref++
		sessionRooms := strings.Split(s.Room, ",")
		found := false
		for _, r := range sessionRooms {
			if strings.TrimSpace(r) == preferred {
				found = true
				break
			}
		}
		if found {
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
	return res.HardScore*0.60 + res.PreferenceScore*0.20 +
		res.DistributionScore*0.10 + res.DefaultRoomScore*0.10
}

func calculateHealth(res *EvaluationResult, totalSessions int) string {
	if res.HiddenSessions > 10 || (totalSessions > 0 && res.HiddenSessions > int(float64(totalSessions)*0.2)) {
		return "low"
	}
	if res.OverallScore >= 85 {
		return "high"
	}
	if res.OverallScore >= 60 {
		return "medium"
	}
	return "low"
}

func parseSessionIdentifier(identifier string) (lessonID string, distIdx int) {
	idx := strings.LastIndex(identifier, "-")
	if idx < 0 {
		return identifier, 0
	}
	lessonID = identifier[:idx]
	distIdx, err := strconv.Atoi(identifier[idx+1:])
	if err != nil {
		return identifier, 0
	}
	return lessonID, distIdx
}

// Time comparison helpers — converts "8:00am" to minutes since midnight.
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

// Session end time: start + 40 minutes (standard lesson duration).
func sessionEndMinutes(s models.Session) int {
	return timeToMinutes(s.Time) + 40
}

func isSessionNotBeforePeriod(s models.Session, periodTitle string, params *EvaluationParams) bool {
	for _, b := range params.Blocks {
		if b.Title == periodTitle {
			periodStart := timeToMinutes(b.Start)
			periodDuration := b.Duration.Hours*60 + b.Duration.Minutes + b.Duration.Duration
			if periodDuration <= 0 {
				periodDuration = 40 // default
			}
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
			breakDuration := b.Duration.Hours*60 + b.Duration.Minutes + b.Duration.Duration
			if breakDuration <= 0 {
				breakDuration = 15 // default break
			}
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


