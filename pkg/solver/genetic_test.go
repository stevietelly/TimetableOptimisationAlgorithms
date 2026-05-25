package solver

import (
	"context"
	"geliana-go/pkg/models"
	"testing"
)

func TestGeneticSolver_MergedLessonOutput(t *testing.T) {
	// 1. Setup a request with a merged lesson
	req := &models.Request{
		Days:   []string{"Monday"},
		Blocks: []models.Block{{Title: "8:00am", Start: "8:00am", Type: "lesson"}},
		Rooms:  []models.Room{{ID: "R1", Title: "Room 1"}},
		Lessons: []models.Lesson{
			{
				Identifier:   "MIC117",
				Unit:         "MIC117",
				Instructor:   "INST1",
				Distribution: []int{1},
				Type:         "lesson-merge",
				MultipleIDs: []models.SubgroupID{
					{GroupID: "G1"},
					{GroupID: "G2"},
				},
			},
		},
	}

	s := NewGeneticSolver(10, 10)
	res, err := s.Solve(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("Solve failed: %v", err)
	}

	sessions := res.Sessions.([]models.Session)
	if len(sessions) != 1 {
		t.Fatalf("Expected 1 session, got %d", len(sessions))
	}

	sess := sessions[0]
	if len(sess.AffectedGroups) != 2 {
		t.Errorf("Expected 2 affected groups, got %d: %v", len(sess.AffectedGroups), sess.AffectedGroups)
	}
	
	// Check if G1 and G2 are present
	gMap := make(map[string]bool)
	for _, g := range sess.AffectedGroups {
		gMap[g] = true
	}
	if !gMap["G1"] || !gMap["G2"] {
		t.Errorf("Expected groups G1 and G2 in AffectedGroups, got %v", sess.AffectedGroups)
	}
}

func TestGeneticSolver_SubgroupSynchronisation(t *testing.T) {
	// Subgroups share the same LessonID and DistIdx and should be merged into one Session
	req := &models.Request{
		Days:   []string{"Monday"},
		Blocks: []models.Block{{Title: "8:00am", Start: "8:00am", Type: "lesson"}},
		Rooms:  []models.Room{{ID: "R1", Title: "Room 1"}},
		Lessons: []models.Lesson{
			{
				Identifier:   "SUB1",
				Unit:         "UNIT1",
				Distribution: []int{1},
				Type:         "subgroup",
				MultipleIDs: []models.SubgroupID{
					{GroupID: "G1-A", InstID: "I1", UnitID: "UNIT1"},
					{GroupID: "G1-B", InstID: "I2", UnitID: "UNIT1"},
				},
			},
		},
	}

	s := NewGeneticSolver(10, 10)
	res, err := s.Solve(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("Solve failed: %v", err)
	}

	sessions := res.Sessions.([]models.Session)
	// They should be merged into ONE session because they share LessonID and DistIdx
	if len(sessions) != 1 {
		t.Fatalf("Expected 1 merged session for subgroups, got %d", len(sessions))
	}

	sess := sessions[0]
	if len(sess.AffectedGroups) != 2 {
		t.Errorf("Expected 2 affected groups in merged subgroup session, got %d", len(sess.AffectedGroups))
	}
	if len(sess.AffectedInstructors) != 2 {
		t.Errorf("Expected 2 affected instructors in merged subgroup session, got %d: %v", len(sess.AffectedInstructors), sess.AffectedInstructors)
	}
}
