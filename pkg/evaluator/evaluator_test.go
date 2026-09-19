package evaluator

import (
	"geliana-go/pkg/models"
	"testing"
)

func TestEvaluateClashes_MergedLesson(t *testing.T) {
	// 1. Define a merged lesson session (MIC117) affecting groups G1 and G2.
	mergedSess := models.Session{
		Identifier:     "MIC117-0",
		Unit:           "MIC117",
		Day:            "Monday",
		Time:           "8:00am",
		Blocks:         1,
		AffectedGroups: []string{"G1", "G2"},
	}

	// 2. Define a regular lesson session for G1 at the same time.
	clashingSess := models.Session{
		Identifier: "REG-0",
		Group:      "G1",
		Unit:       "REG1",
		Day:        "Monday",
		Time:       "8:00am",
		Blocks:     1,
	}

	sessions := []models.Session{mergedSess, clashingSess}
	params := &EvaluationParams{
		Days:        []string{"Monday"},
		PeriodTimes: []string{"8:00am"},
	}

	res := Evaluate(sessions, params)

	if res.TotalClashes == 0 {
		t.Errorf("Expected clash between merged lesson and regular lesson for Group G1, but found none")
	}

	foundGroupClash := false
	for _, v := range res.Violations {
		if v.Type == "group_clash" {
			foundGroupClash = true
		}
	}
	if !foundGroupClash {
		t.Errorf("Expected group_clash violation, but none found")
	}
}

func TestEvaluateClashes_SubgroupMultipleIDs(t *testing.T) {
	// Subgroup elective with 2 instructors and 2 rooms
	sgSess := models.Session{
		Identifier: "SG-01",
		Group:      "G1",
		Day:        "Monday",
		Time:       "8:00am",
		Blocks:     1,
		Multiple:   true,
		Type:       "subgroup",
		MultipleIDs: []models.SubgroupID{
			{GroupID: "G1", UnitID: "ART", InstID: "T_ART", RoomID: "R_ART"},
			{GroupID: "G1", UnitID: "MUS", InstID: "T_MUS", RoomID: "R_MUS"},
		},
	}

	// Regular session with T_MUS in a different group at same time -> instructor clash
	clashingInstSess := models.Session{
		Identifier: "REG-INST",
		Group:      "G2",
		Instructor: "T_MUS",
		Unit:       "MUS2",
		Room:       "R_OTHER",
		Day:        "Monday",
		Time:       "8:00am",
		Blocks:     1,
	}

	// Regular session in R_ART at same time -> room clash
	clashingRoomSess := models.Session{
		Identifier: "REG-ROOM",
		Group:      "G3",
		Instructor: "T_OTHER",
		Unit:       "OTHER",
		Room:       "R_ART",
		Day:        "Monday",
		Time:       "8:00am",
		Blocks:     1,
	}

	sessions := []models.Session{sgSess, clashingInstSess, clashingRoomSess}
	params := &EvaluationParams{
		Days:        []string{"Monday"},
		PeriodTimes: []string{"8:00am"},
	}

	res := Evaluate(sessions, params)

	hasInstClash := false
	hasRoomClash := false
	for _, v := range res.Violations {
		if v.Type == "instructor_clash" {
			hasInstClash = true
		}
		if v.Type == "room_clash" {
			hasRoomClash = true
		}
	}

	if !hasInstClash {
		t.Errorf("Expected instructor_clash with subgroup elective instructor T_MUS, but found none")
	}
	if !hasRoomClash {
		t.Errorf("Expected room_clash with subgroup elective room R_ART, but found none")
	}
}

func TestEvaluatePreferences_MergedLesson(t *testing.T) {
	pref := models.Preference{
		Type: "ONLY",
		Target: models.PreferenceTarget{
			Kind:  "DAY",
			Value: "Tuesday",
		},
	}

	params := &EvaluationParams{
		Days:        []string{"Monday", "Tuesday"},
		PeriodTimes: []string{"8:00am"},
		Groups: []models.Group{
			{ID: "G2", Preferences: []models.Preference{pref}},
		},
	}

	mergedSess := models.Session{
		Identifier:     "MIC117-0",
		Unit:           "MIC117",
		Day:            "Monday",
		Time:           "8:00am",
		Blocks:         1,
		AffectedGroups: []string{"G1", "G2"},
	}

	res := Evaluate([]models.Session{mergedSess}, params)

	if res.PreferenceScore >= 100 {
		t.Errorf("Expected preference violation for G2 (ONLY Tuesday) in merged lesson on Monday, but score is 100")
	}
}

func TestEvaluatePreferences_RoomExactMatch(t *testing.T) {
	// Preference: ONLY in Room 1
	pref := models.Preference{
		Type: "ONLY",
		Target: models.PreferenceTarget{
			Kind:  "ROOM",
			Value: "Room 1",
		},
	}

	params := &EvaluationParams{
		Days:        []string{"Monday"},
		PeriodTimes: []string{"8:00am"},
		Units: []models.Unit{
			{ID: "MATH", Preferences: []models.Preference{pref}},
		},
	}

	// Session in "Room 10" must violate "ONLY Room 1" (preventing substring match bug)
	sess := models.Session{
		Identifier: "S1",
		Group:      "G1",
		Unit:       "MATH",
		Room:       "Room 10",
		Day:        "Monday",
		Time:       "8:00am",
		Blocks:     1,
	}

	res := Evaluate([]models.Session{sess}, params)

	if res.PreferenceScore >= 100 {
		t.Errorf("Expected preference violation because session is in 'Room 10' not 'Room 1', but got 100")
	}
}

func TestEvaluateDistribution_ContiguousRuns(t *testing.T) {
	lesson := models.Lesson{
		Identifier:   "L1",
		Group:        "G1",
		Unit:         "ENG",
		Instructor:   "T1",
		TotalLessons: 3,
		Distribution: []int{2, 1}, // Desired: 1 double + 1 single
	}

	params := &EvaluationParams{
		Days:        []string{"Monday", "Wednesday"},
		PeriodTimes: []string{"8:00am", "8:40am", "9:20am"},
		Lessons:     []models.Lesson{lesson},
	}

	// Two sessions contiguous on Monday (8:00am, 8:40am) + one session on Wednesday (8:00am)
	// Random identifiers (UUID style) to verify it does not depend on id parsing
	sessions := []models.Session{
		{
			Identifier: "uuid-sess-1",
			Group:      "G1",
			Unit:       "ENG",
			Instructor: "T1",
			Day:        "Monday",
			Time:       "8:00am",
			Blocks:     1,
		},
		{
			Identifier: "uuid-sess-2",
			Group:      "G1",
			Unit:       "ENG",
			Instructor: "T1",
			Day:        "Monday",
			Time:       "8:40am",
			Blocks:     1,
		},
		{
			Identifier: "uuid-sess-3",
			Group:      "G1",
			Unit:       "ENG",
			Instructor: "T1",
			Day:        "Wednesday",
			Time:       "8:00am",
			Blocks:     1,
		},
	}

	res := Evaluate(sessions, params)

	if res.DistributionScore < 100.0 {
		t.Errorf("Expected distribution score 100.0 for double + single, got %.2f", res.DistributionScore)
	}
	if res.MissingSessions != 0 || res.ExtraSessions != 0 {
		t.Errorf("Expected 0 missing and 0 extra sessions, got missing=%d extra=%d", res.MissingSessions, res.ExtraSessions)
	}
	if res.Health != "high" {
		t.Errorf("Expected health 'high', got %q", res.Health)
	}
}

func TestEvaluateDistribution_MissingAndExtraSessions(t *testing.T) {
	lesson := models.Lesson{
		Identifier:   "L_SHORT",
		Group:        "G1",
		Unit:         "SCI",
		Instructor:   "T1",
		TotalLessons: 5,
		Distribution: []int{2, 2, 1},
	}

	params := &EvaluationParams{
		Days:        []string{"Monday", "Tuesday"},
		PeriodTimes: []string{"8:00am", "8:40am"},
		Lessons:     []models.Lesson{lesson},
	}

	// Only 1 session scheduled out of 5 required
	sessions := []models.Session{
		{
			Identifier: "s1",
			Group:      "G1",
			Unit:       "SCI",
			Instructor: "T1",
			Day:        "Monday",
			Time:       "8:00am",
			Blocks:     1,
		},
	}

	res := Evaluate(sessions, params)

	if res.MissingSessions != 4 {
		t.Errorf("Expected 4 missing sessions, got %d", res.MissingSessions)
	}
	// Total sessions = 1, missing = 4 (>20%), health must be demoted
	if res.Health != "low" && res.Health != "medium" {
		t.Errorf("Expected health demotion due to missing sessions, got %q", res.Health)
	}
}

func TestEvaluateDefaultRooms_MultipleIDs(t *testing.T) {
	lesson := models.Lesson{
		Identifier:  "L_SG",
		Group:       "G1",
		Unit:        "ART",
		DefaultRoom: "Room_Art",
	}

	params := &EvaluationParams{
		Days:        []string{"Monday"},
		PeriodTimes: []string{"8:00am"},
		Lessons:     []models.Lesson{lesson},
		Units: []models.Unit{
			{ID: "ART", DefaultRoom: "Room_Art"},
		},
	}

	// Subgroup session whose room is in MultipleIDs
	sess := models.Session{
		Identifier: "SG-ROOM",
		Group:      "G1",
		Unit:       "ART",
		Day:        "Monday",
		Time:       "8:00am",
		Blocks:     1,
		Multiple:   true,
		Type:       "subgroup",
		MultipleIDs: []models.SubgroupID{
			{GroupID: "G1", UnitID: "ART", InstID: "T1", RoomID: "Room_Art"},
		},
	}

	res := Evaluate([]models.Session{sess}, params)

	if res.DefaultRoomScore < 100.0 {
		t.Errorf("Expected default room score 100.0, got %.2f", res.DefaultRoomScore)
	}
}
