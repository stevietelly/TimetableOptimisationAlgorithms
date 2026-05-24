package models

import "time"

// Request represents the root optimization request payload.
type Request struct {
	UserID         string       `json:"user_id"`
	Timestamp      time.Time    `json:"timestamp"`
	Mode           string       `json:"mode"` // "timetable" or "timesheet"
	MultiTimetable bool         `json:"multi_timetable"`
	Lessons        []Lesson     `json:"lessons"`
	Rooms          []Room       `json:"rooms"`
	Instructors    []Instructor `json:"instructors"`
	Groups         []Group      `json:"groups"`
	Units          []Unit       `json:"units"`
	Days           []string     `json:"days"`    // Root level (preferred)
	Periods        []string     `json:"periods"` // Root level
	Blocks         []Block      `json:"blocks"`  // Root level
	Breaks         []Break      `json:"breaks"`  // Root level
	
	Configuration Configuration `json:"configuration"` // Nested configuration
	
	// Optional Multi-timetable metadata
	TimetablesMetadata []TimetableMetadata `json:"timetables_metadata,omitempty"`
}

// Configuration represents schedule settings.
type Configuration struct {
	Days      []string `json:"days"`
	Periods   []string `json:"periods"`
	StartTime string   `json:"startTime"`
	EndTime   string   `json:"endTime"`
}

// Normalize ensures that critical fields are populated from either root or configuration.
func (r *Request) Normalize() {
	if len(r.Days) == 0 && len(r.Configuration.Days) > 0 {
		r.Days = r.Configuration.Days
	}
	if len(r.Periods) == 0 && len(r.Configuration.Periods) > 0 {
		r.Periods = r.Configuration.Periods
	}
	
	// Handle Legacy to Lesson-based conversion
	if len(r.Lessons) == 0 && len(r.Groups) > 0 {
		r.LegacyToLessonBased()
	}
}

// LegacyToLessonBased converts units and groups into a modern lesson-based format.
func (r *Request) LegacyToLessonBased() {
	// Generate blocks if missing
	if len(r.Blocks) == 0 {
		// Simple hourly block generation as a fallback
		r.Blocks = []Block{
			{Title: "8:00am", Start: "8:00am", Type: "lesson", Index: 0},
			{Title: "9:00am", Start: "9:00am", Type: "lesson", Index: 1},
			{Title: "10:00am", Start: "10:00am", Type: "lesson", Index: 2},
			{Title: "11:00am", Start: "11:00am", Type: "lesson", Index: 3},
			{Title: "12:00pm", Start: "12:00pm", Type: "lesson", Index: 4},
			{Title: "1:00pm", Start: "1:00pm", Type: "lesson", Index: 5},
			{Title: "2:00pm", Start: "2:00pm", Type: "lesson", Index: 6},
			{Title: "3:00pm", Start: "3:00pm", Type: "lesson", Index: 7},
		}
	}

	unitToInstructors := make(map[string][]string)
	unitToLessons := make(map[string]int)
	for _, u := range r.Units {
		unitToInstructors[u.ID] = u.Instructors
		unitToLessons[u.ID] = u.Lessons
	}

	for _, g := range r.Groups {
		// Process units based on interface type
		var groupUnits []struct{ uID, iID string }

		switch v := g.Units.(type) {
		case []string:
			for _, id := range v {
				groupUnits = append(groupUnits, struct{ uID, iID string }{uID: id})
			}
		case []interface{}:
			for _, item := range v {
				if id, ok := item.(string); ok {
					groupUnits = append(groupUnits, struct{ uID, iID string }{uID: id})
				} else if obj, ok := item.(map[string]interface{}); ok {
					uID, _ := obj["unitID"].(string)
					iID, _ := obj["instructorID"].(string)
					groupUnits = append(groupUnits, struct{ uID, iID string }{uID: uID, iID: iID})
				}
			}
		}

		for _, gu := range groupUnits {
			lessonsCount := unitToLessons[gu.uID]
			if lessonsCount == 0 {
				lessonsCount = 1
			}

			dist := make([]int, len(r.Days))
			for i := 0; i < lessonsCount && i < len(r.Days); i++ {
				dist[i] = 1
			}

			instructor := gu.iID
			if instructor == "" {
				if insts, ok := unitToInstructors[gu.uID]; ok && len(insts) > 0 {
					instructor = insts[0]
				}
			}

			r.Lessons = append(r.Lessons, Lesson{
				Identifier:   g.ID + "_" + gu.uID,
				Group:        g.ID,
				Instructor:   instructor,
				Unit:         gu.uID,
				TotalLessons: lessonsCount,
				Distribution: dist,
				Type:         "regular",
			})
		}
	}
}

// Lesson represents a scheduling requirement.
type Lesson struct {
	Identifier    string            `json:"identifier"`
	Group         string            `json:"group"`
	Instructor    string            `json:"instructor"`
	Unit          string            `json:"unit"`
	TotalLessons  int               `json:"total_lessons"`
	Distribution  []int             `json:"distribution"`
	Online        bool              `json:"online"`
	DefaultRoom   string            `json:"default_room"`
	Multiple      bool              `json:"multiple"`
	Type          string            `json:"type"` // "regular", "lesson-merge", "subgroup", etc.
	Short         string            `json:"short"`
	Title         string            `json:"title"`
	GroupTotal    int               `json:"group_total"`
	DatabaseIDs   LessonDatabaseIDs `json:"database_ids"`
	TimetableID   string            `json:"timetable_id,omitempty"`
	MultipleIDs   []SubgroupID      `json:"multiple_ids,omitempty"`
}

type LessonDatabaseIDs struct {
	Group      string `json:"group"`
	Instructor string `json:"instructor"`
	Unit       string `json:"unit"`
	Room       string `json:"room"`
}

type SubgroupID struct {
	GroupID        string `json:"group_id"`
	InstID         string `json:"inst_id"`
	UnitID         string `json:"unit_id"`
	RoomID         string `json:"room_id"`
	DivisionID     string `json:"division_id,omitempty"`
	SubDivisionID  string `json:"sub_division_id,omitempty"`
	AffectedGroups []string `json:"affected_groups,omitempty"`
}

// Room represents a physical location for lessons.
type Room struct {
	ID          string       `json:"id"`
	Title       string       `json:"title"`
	Capacity    int          `json:"capacity"`
	Color       string       `json:"color"`
	Preferences []Preference `json:"preferences"`
	DatabaseID  string       `json:"database_id"`
}

// Instructor represents a teacher or lecturer.
type Instructor struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Title       string       `json:"title"`
	Short       string       `json:"short"`
	Color       string       `json:"color"`
	Preferences []Preference `json:"preferences"`
	DatabaseID  string       `json:"database_id"`
}

// Group represents a class of students.
type Group struct {
	ID          string       `json:"id"`
	Title       string       `json:"title"`
	Total       int          `json:"total"`
	Color       string       `json:"color"`
	Preferences []Preference `json:"preferences"`
	Units       interface{}  `json:"units"` // interface{} to support []string or []GroupUnit
	DatabaseID  string       `json:"database_id"`
}

// GroupUnit represents a unit-instructor mapping within a group (object-based legacy).
type GroupUnit struct {
	UnitID       string `json:"unitID"`
	InstructorID string `json:"instructorID"`
}

// Unit represents a subject or course.
type Unit struct {
	ID          string       `json:"id"`
	Title       string       `json:"title"`
	Short       string       `json:"short"`
	Lessons     int          `json:"lessons"`
	Color       string       `json:"color"`
	Online      bool         `json:"online"`
	Preferences []Preference `json:"preferences"`
	Instructors []string     `json:"instructors"` // Added for legacy support
	DatabaseID  string       `json:"database_id"`
}

// Block represents a time period in the schedule.
type Block struct {
	Type     string   `json:"type"` // "lesson" or "period"
	Title    string   `json:"title"`
	Start    string   `json:"start"`
	Duration Duration `json:"duration"`
	Index    int      `json:"index"`
}

// Break represents a scheduled pause.
type Break struct {
	Title     string   `json:"title"`
	StartTime string   `json:"startTime"`
	Duration  Duration `json:"duration"`
}

// Duration represents a length of time.
type Duration struct {
	Hours    int `json:"hours,omitempty"`
	Minutes  int `json:"minutes,omitempty"`
	Duration int `json:"duration,omitempty"` // Alternative field name seen in some JSONs
}

// Preference represents a constraint or soft preference.
type Preference struct {
	Type    string           `json:"type"` // "BEFORE", "AFTER", "EXCEPT", "ONLY", etc.
	Target  PreferenceTarget `json:"target"`
	Days    []string         `json:"days,omitempty"`
	Times   []string         `json:"times,omitempty"`
	RoomIDs []string         `json:"room_ids,omitempty"`
}

type PreferenceTarget struct {
	Kind  string `json:"kind"`  // "DAY", "TIME", "ROOM"
	Label string `json:"label"`
	Value string `json:"value"`
}

// TimetableMetadata represents configuration for a specific timetable in multi-timetable mode.
type TimetableMetadata struct {
	TimetableID       string   `json:"timetable_id"`
	Title             string   `json:"title"`
	Days              []string `json:"days"`
	Times             []string `json:"times"`
	Blocks            []Block  `json:"blocks"`
	CalendarStartDay string   `json:"calendar_start_day,omitempty"`
	CalendarEndDay   string   `json:"calendar_end_day,omitempty"`
}

// Session represents a scheduled lesson in the output.
type Session struct {
	Identifier  string            `json:"identifier"`
	Group       string            `json:"group"`
	Instructor  string            `json:"instructor"`
	Unit        string            `json:"unit"`
	Day         string            `json:"day"`
	Time        string            `json:"time"`
	Room        string            `json:"room"`
	Online      bool              `json:"online"`
	Blocks      int               `json:"blocks,omitempty"` // consecutive blocks this session occupies
	DatabaseIDs LessonDatabaseIDs `json:"database_ids"`
	TimetableID string            `json:"timetable_id,omitempty"`
}
// Response represents the optimization result.
type Response struct {
	Error    bool                 `json:"error"`
	Message  string               `json:"message"`
	Sessions interface{}          `json:"sessions"` // []Session or map[string][]Session
	Stats    OptimizationStats    `json:"stats"`
	Trace    []TraceStep          `json:"trace,omitempty"` // Search path trace
}

// TraceStep represents a single point in the solver's search path.
type TraceStep struct {
	Step      int                    `json:"step"`
	Type      string                 `json:"type"` // "assignment", "backtrack", "generation", "iteration"
	Label     string                 `json:"label"`
	Score     float64                `json:"score"`
	Timestamp float64                `json:"timestamp"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

type OptimizationStats struct {
	OverallScore    float64            `json:"overall_score"`
	TimeTaken       float64            `json:"time_taken"`
	MemoryUsageMB   float64            `json:"memory_usage_mb"`
	SolutionFound   bool               `json:"solution_found"`
	FailReason      string             `json:"fail_reason,omitempty"`
	TimetableScores map[string]float64 `json:"timetable_scores,omitempty"`
}
