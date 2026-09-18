package solver

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"geliana-go/pkg/models"
)

func heuristicVarietyRequest() *models.Request {
	return &models.Request{
		Days: []string{"Monday", "Tuesday", "Wednesday"},
		Blocks: []models.Block{
			{Title: "8:00am", Start: "8:00am", Type: "lesson"},
			{Title: "9:00am", Start: "9:00am", Type: "lesson"},
		},
		Rooms: []models.Room{
			{ID: "R1", Title: "Room 1"},
			{ID: "R2", Title: "Room 2"},
		},
		Groups: []models.Group{
			{ID: "G1", Title: "Group 1"},
			{ID: "G2", Title: "Group 2"},
		},
		Lessons: []models.Lesson{
			{
				Identifier:   "L1",
				Group:        "G1",
				Instructor:   "I1",
				Unit:         "U1",
				Distribution: []int{1, 1},
				Type:         "regular",
			},
			{
				Identifier:   "L2",
				Group:        "G2",
				Instructor:   "I2",
				Unit:         "U2",
				Distribution: []int{1},
				Type:         "regular",
			},
		},
	}
}

func heuristicLayoutSignature(t *testing.T, req *models.Request, seed int64, shuffle string) string {
	t.Helper()
	r := *req
	r.Seed = seed
	r.Shuffle = shuffle
	s := NewHeuristicCPSolver(MRVDegree, LeastLoadedDay)
	res, err := s.Solve(context.Background(), &r, nil)
	if err != nil {
		t.Fatalf("Solve(seed=%d, shuffle=%q) failed: %v", seed, shuffle, err)
	}
	sessions, ok := res.Sessions.([]models.Session)
	if !ok {
		t.Fatalf("Sessions has type %T, want []models.Session", res.Sessions)
	}
	parts := make([]string, 0, len(sessions))
	for _, sess := range sessions {
		parts = append(parts, fmt.Sprintf("%s@%s %s %s", sess.Identifier, sess.Day, sess.Time, sess.Room))
	}
	return strings.Join(parts, "|")
}

func TestNormalizeShuffleMode(t *testing.T) {
	cases := map[string]string{
		"":       "ties",
		"ties":   "ties",
		"TIES":   "ties",
		" full ": "full",
		"off":    "off",
		"OFF":    "off",
		"bogus":  "ties",
		"random": "ties",
	}
	for in, want := range cases {
		if got := normalizeShuffleMode(in); got != want {
			t.Errorf("normalizeShuffleMode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHeuristicCP_SeededDeterminism(t *testing.T) {
	req := heuristicVarietyRequest()
	first := heuristicLayoutSignature(t, req, 12345, "")
	second := heuristicLayoutSignature(t, req, 12345, "")
	if first != second {
		t.Errorf("same seed gave different layouts:\n%s\n%s", first, second)
	}
}

func TestHeuristicCP_ShuffleOffDeterministic(t *testing.T) {
	req := heuristicVarietyRequest()
	first := heuristicLayoutSignature(t, req, 999, "off")
	second := heuristicLayoutSignature(t, req, 999, "off")
	if first != second {
		t.Errorf("shuffle=off gave different layouts:\n%s\n%s", first, second)
	}
	// Legacy order anchor: Sequential value order starts at day 0, block 0.
	r := *req
	r.Seed = 999
	r.Shuffle = "off"
	s := NewHeuristicCPSolver(Sequential, SequentialValue)
	res, err := s.Solve(context.Background(), &r, nil)
	if err != nil {
		t.Fatalf("Solve failed: %v", err)
	}
	sessions := res.Sessions.([]models.Session)
	if sessions[0].Day != "Monday" || sessions[0].Time != "8:00am" {
		t.Errorf("shuffle=off changed legacy order: first session at %s %s, want Monday 8:00am",
			sessions[0].Day, sessions[0].Time)
	}
}

func TestHeuristicCP_DifferentSeedsVary(t *testing.T) {
	req := heuristicVarietyRequest()
	seen := make(map[string]bool)
	for seed := int64(1); seed <= 8; seed++ {
		seen[heuristicLayoutSignature(t, req, seed, "ties")] = true
	}
	if len(seen) < 2 {
		t.Errorf("8 seeds produced a single layout; tie-shuffling has no effect")
	}
}

func TestHeuristicCP_RestartsReportAttempts(t *testing.T) {
	r := *heuristicVarietyRequest()
	r.Seed = 7
	r.Restarts = 3
	s := NewHeuristicCPSolver(MRVDegree, LeastLoadedDay)
	res, err := s.Solve(context.Background(), &r, nil)
	if err != nil {
		t.Fatalf("Solve failed: %v", err)
	}
	if !strings.Contains(res.Message, "attempts=") {
		t.Errorf("response message missing attempts report: %q", res.Message)
	}
	if !strings.Contains(res.Message, "seed=7") {
		t.Errorf("response message missing pinned seed: %q", res.Message)
	}
	sessions := res.Sessions.([]models.Session)
	if len(sessions) != 3 {
		t.Errorf("expected 3 sessions, got %d", len(sessions))
	}
}

func TestHeuristicCP_OnlinePropagatedToMultipleIDs(t *testing.T) {
	req := &models.Request{
		Days:   []string{"Monday"},
		Blocks: []models.Block{{Title: "8:00am", Start: "8:00am", Type: "lesson"}},
		Rooms:  []models.Room{{ID: "R1", Title: "Room 1"}},
		Lessons: []models.Lesson{
			{
				Identifier:   "ONL1",
				Unit:         "U1",
				Instructor:   "I1",
				Online:       true,
				Distribution: []int{1},
				Type:         "lesson-merge",
				MultipleIDs: []models.SubgroupID{
					{GroupID: "G1"},
					{GroupID: "G2"},
				},
			},
		},
	}
	s := NewHeuristicCPSolver(Sequential, SequentialValue)
	res, err := s.Solve(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("Solve failed: %v", err)
	}
	sessions := res.Sessions.([]models.Session)
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if !sessions[0].Online {
		t.Errorf("session root Online = false, want true")
	}
	for _, m := range sessions[0].MultipleIDs {
		if !m.Online {
			t.Errorf("multiple_ids entry %+v Online = false, want true", m)
		}
	}
}
