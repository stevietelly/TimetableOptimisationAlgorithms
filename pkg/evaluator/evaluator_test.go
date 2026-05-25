package evaluator

import (
	"geliana-go/pkg/models"
	"testing"
)

func TestEvaluateClashes_MergedLesson(t *testing.T) {
	// 1. Define a merged lesson session (MIC117)
	// It affects groups G1 and G2.
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

func TestEvaluatePreferences_MergedLesson(t *testing.T) {
	// Preference: G2 ONLY on Tuesday
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

	// MIC117 on Monday affecting G1 and G2
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
