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

	Days    []string `json:"days"`    // Root level (preferred)
	Periods []string `json:"periods"` // Root level
	Blocks  []Block  `json:"blocks"`  // Root level
	Breaks  []Break  `json:"breaks"`  // Root level


	Configuration Configuration `json:"configuration"` // Nested configuration

	// Optional Multi-timetable metadata
	TimetablesMetadata []TimetableMetadata `json:"timetables_metadata,omitempty"`

	// flags
	OptimiseDefaultRooms bool `json:"optimise_default_rooms"`
	OptimiseRoomCapacity bool `json:"optimise_room_capacity"`
	OptimisePreferences bool `json:"optimise_preferences"`

	// Variety controls (heuristic_cp only).
	// Seed pins the solver RNG: 0 (absent) means "pick a random seed", any
	// nonzero value reproduces that exact run. The effective seed is always
	// echoed back in the response message/metrics.
	Seed int64 `json:"seed,omitempty"`
	// Shuffle selects candidate try-order randomisation: "off" keeps the
	// legacy deterministic order, "ties" (default) shuffles only
	// equal-score candidates, "full" shuffles all candidates per node.
	// Unknown values fall back to "ties".
	Shuffle string `json:"shuffle,omitempty"`
	// Restarts caps random-restart attempts per Solve (best score wins).
	// Values < 1 mean a single attempt.
	Restarts int `json:"restarts,omitempty"`
}

// Configuration represents schedule settings.
type Configuration struct {
	Days      []string `json:"days"`
	Periods   []string `json:"periods"`
	StartTime string   `json:"startTime"`
	EndTime   string   `json:"endTime"`
}

// Lesson represents a scheduling requirement.
type Lesson struct {
	Identifier   string            `json:"identifier"`
	Group        string            `json:"group"`
	Instructor   string            `json:"instructor"`
	Unit         string            `json:"unit"`
	TotalLessons int               `json:"total_lessons"`
	Distribution []int             `json:"distribution"`
	Online       bool              `json:"online"`
	DefaultRoom  string            `json:"default_room"`
	Multiple     bool              `json:"multiple"`
	Type         string            `json:"type"` // "regular", "lesson-merge", "subgroup", etc.
	Short        string            `json:"short"`
	Title        string            `json:"title"`
	GroupTotal   int               `json:"group_total"`
	DatabaseIDs  LessonDatabaseIDs `json:"database_ids"`
	TimetableID  string            `json:"timetable_id,omitempty"`
	MultipleIDs  []SubgroupID      `json:"multiple_ids,omitempty"`
}

type LessonDatabaseIDs struct {
	Group      string `json:"group"`
	Instructor string `json:"instructor"`
	Unit       string `json:"unit"`
	Room       string `json:"room"`
}

type SubgroupID struct {
	GroupID                string   `json:"group_id"`
	GroupDatabase          string   `json:"group_database,omitempty"`
	InstID                 string   `json:"inst_id"`
	InstDatabase           string   `json:"inst_database,omitempty"`
	UnitID                 string   `json:"unit_id"`
	UnitDatabase           string   `json:"unit_database,omitempty"`
	RoomID                 string   `json:"room_id"`
	RoomDatabase           string   `json:"room_database,omitempty"`
	DivisionID             string   `json:"division_id,omitempty"`
	SubDivisionID          string   `json:"sub_division_id,omitempty"`
	Online                 bool     `json:"online"`
	AffectedGroups         []string `json:"affected_groups,omitempty"`
	AffectedGroupDatabases []string `json:"affected_group_databases,omitempty"`
}

// Room represents a physical location for lessons.
type Room struct {
	ID          string       `json:"id"`
	Title       string       `json:"title"`
	Capacity    int          `json:"capacity"`
	Color       string       `json:"color"`
	Preferences []Preference `json:"preferences"`
	DefaultRoom string       `json:"default_room,omitempty"` // Added for completeness
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
	DefaultRoom string       `json:"default_room,omitempty"`
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
	DefaultRoom string       `json:"default_room,omitempty"`
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
	Kind  string `json:"kind"` // "DAY", "TIME", "ROOM"
	Label string `json:"label"`
	Value string `json:"value"`
}

// TimetableMetadata represents configuration for a specific timetable in multi-timetable mode.
type TimetableMetadata struct {
	TimetableID      string   `json:"timetable_id"`
	Title            string   `json:"title"`
	Days             []string `json:"days"`
	Times            []string `json:"times"`
	Blocks           []Block  `json:"blocks"`
	CalendarStartDay string   `json:"calendar_start_day,omitempty"`
	CalendarEndDay   string   `json:"calendar_end_day,omitempty"`
}

// Session represents a scheduled lesson in the output.
type Session struct {
	Identifier string   `json:"identifier"`
	Group      string   `json:"group"`
	Instructor string   `json:"instructor"`
	Unit       string   `json:"unit"`
	Day        string   `json:"day"`
	Time       string   `json:"time"`
	Times      []string `json:"times,omitempty"`
	Room       string   `json:"room"`
	Online     bool     `json:"online"`
	Blocks     int      `json:"blocks,omitempty"` // consecutive blocks this session occupies

	// Multi-entity support for merged lessons and subgroups
	Multiple            bool         `json:"multiple"`
	Type                string       `json:"type"`
	MultipleIDs         []SubgroupID `json:"multiple_ids,omitempty"`
	Short               string       `json:"short"`
	Title               string       `json:"title"`
	AffectedGroups      []string     `json:"affected_groups,omitempty"`
	AffectedInstructors []string     `json:"affected_instructors,omitempty"`

	DatabaseIDs LessonDatabaseIDs `json:"database_ids"`
	TimetableID string            `json:"timetable_id,omitempty"`
}

// Response represents the optimization result.
type Response struct {
	Error    bool              `json:"error"`
	Message  string            `json:"message"`
	Sessions interface{}       `json:"sessions"` // []Session or map[string][]Session
	Stats    OptimizationStats `json:"stats"`
	Trace    []TraceStep       `json:"trace,omitempty"` // Search path trace
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
	OverallScore      float64            `json:"overall_score"`
	TimeTaken         float64            `json:"time_taken"`
	MemoryUsageMB     float64            `json:"memory_usage_mb"`
	SolutionFound     bool               `json:"solution_found"`
	FailReason        string             `json:"fail_reason,omitempty"`
	TimetableScores   map[string]float64 `json:"timetable_scores,omitempty"`
	HardScore         float64            `json:"hard_score"`
	PreferenceScore   float64            `json:"preference_score"`
	DistributionScore float64            `json:"distribution_score"`
	DefaultRoomScore  float64            `json:"default_room_score"`
	TotalClashes      int                `json:"total_clashes"`
	HiddenSessions    int                `json:"hidden_sessions"`
	Health            string             `json:"health"`
}
