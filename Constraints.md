# Constraints & Scoring

Reference implementation from `src/assets/Algorithms/Evaluation.ts` (TypeScript).  
This document catalogs every constraint for the Go-based evaluator.

---

## Overall Score Formula

```
overall = (hard / 100) * 60 + (preferences / 100) * 20 + (distribution / 100) * 10 + (default_rooms / 100) * 10
```

| Component | Weight | Category |
|-----------|--------|----------|
| Hard Constraints | 60% | Clashes |
| Preferences | 20% | Soft |
| Lesson Distribution | 10% | Soft |
| Default Rooms | 10% | Soft |

**Health tiers**: `>= 85` → high, `>= 60` → medium, `< 60` → low

---

## 1. Hard Constraints — Clashes (60% weight)

Clash ratio = `totalClashes / len(sessions)`.  
Score = `max(0, 100 * (1 - min(1, clashRatio * 2)))`.  
Clashes are combinatorial: `n` sessions at the same day+time for the same entity → `n * (n-1) / 2` clashes.

### 1a. Group Clash
- Two or more sessions share the same `group` on the same `day` + `time`.
- **Regular sessions**: compare `session.group` directly.
- **Multiple/merged sessions**: check each `multiple_ids[].group_id` and each entry in `affected_groups`.
- **Online sessions**: still checked (online doesn't exempt group clashes).

### 1b. Instructor Clash
- Two or more sessions share the same `instructor` on the same `day` + `time`.
- **Regular sessions**: compare `session.instructor`.
- **Subgroup/multiple sessions**: check each `multiple_ids[].inst_id`.

### 1c. Room Clash
- Two or more sessions share the same `room` on the same `day` + `time`.
- **Skip** if `session.online === true` or `mid.online === true`.
- Handle comma-separated room strings (e.g., `"R101,R102"`) — split and check each.
- **Regular sessions**: compare `session.room`.
- **Multiple sessions**: check each `multiple_ids[].room_id`.

### 1d. Hidden Session (affects health, not component score)
- Session's `day` not found in the valid day list OR its `time` not found in `periodTimes`.
- **If hidden > 10 OR hidden > sessions * 0.2** → health forced to `low`.
- Otherwise if health was `high` → demoted to `medium`.

---

## 2. Soft Constraint — Preferences (20% weight)

```
preferences_score = ((totalChecks - violations) / totalChecks) * 100
```

Each session is checked against preferences on **three entity types**: group, unit, and instructor.  
Each entity can hold multiple preferences. Each preference has a `type`, `kind`, and `value`.

### Preference Types

| Type | Logic | Kinds |
|------|-------|-------|
| **ONLY** | Violation if session does NOT match target | DAY, TIME, ROOM |
| **EXCEPT** | Violation if session DOES match target | DAY, TIME, ROOM |
| **BEFORE** | Violation if session is at/after target | DAY, TIME, PERIOD, BREAK |
| **AFTER** | Violation if session is at/before target | DAY, TIME, PERIOD, BREAK |

### Kind-specific rules

| Kind | Check |
|------|-------|
| **DAY** | Compare `session.day` (long name, lowercased) against target value |
| **TIME** | Compare `session.time` start against target time (exact match for ONLY/EXCEPT, inequality for BEFORE/AFTER) |
| **ROOM** | Compare `session.room` against target room string |
| **PERIOD** | Session end time (`start + 40min`) vs period start/end time |
| **BREAK** | Session start/end vs break start time + break duration |

---

## 3. Soft Constraint — Lesson Distribution (10% weight)

```
distribution_score = (wellDistributed / totalLessons) * 100
```

Each lesson has a `desiredDistribution` array (counts per block) and a `periodBlocks` structure.

### Fit Calculation
1. **Zero sessions** → `fit = 0.0`
2. **Perfect match** (`desiredTotal == actualTotal`, block sizes match exactly) → `fit = 1.0`
3. **Partial match** (same total, different blocks) → per-block `match = 1 - |desired - actual| / desired`, then `fit = matchSum / blockCount`
4. **Wrong total** → `countMatch = max(0, 1 - |actual - desired| / max(1, desired))`, then `fit = countMatch * 0.5` (capped)
5. **Well-distributed** classification → `fit >= 0.8`

**Skip**: Lessons with empty distribution or all-zeros.

---

## 4. Soft Constraint — Default Room Satisfaction (10% weight)

```
default_rooms_score = (satisfied / totalWithPreference) * 100
```

For each non-online session, check if its assigned room matches the preferred room.

### Priority of default_room lookup
1. **Lesson's `default_room`** — match by `lesson.group == session.group && lesson.unit == session.unit`
2. **Unit's `default_room`** — via `unitMap.get(session.unit)`
3. **Group's `default_room`** — via `groupMap.get(session.group)`

**Skip**: online sessions. Comma-separated rooms are split and each checked.

---

## 5. Structural Session Validation (Pre-evaluation)

These produce **errors** (block evaluation) or **warnings** (allow evaluation but report issues).

### Common (all sessions)
- Missing required fields: `identifier`, `day`, `time`, `type`, `multiple`
- Empty/null sessions array

### Regular sessions
- Missing `room` when `online == false` → error
- Non-empty `room` when `online == true` → warning
- Missing `group` / `unit` / `instructor` → error
- Incorrect `multiple` flag → warning
- Non-empty `multiple_ids` → warning

### Lesson-Merge sessions
- Missing `unit` or `instructor` → error
- `multiple != true` → error
- Missing/empty `multiple_ids` → error
- Any `multiple_ids[]` entry missing `group_id`, `unit_id`, or `inst_id` → error
- Non-online entry missing `room_id` → error
- Online entry with non-empty `room_id` → warning

### Subgroup / Subgroup-Lesson-Merge sessions
- `multiple != true` → error
- Missing/empty `multiple_ids` → error
- Any entry missing `group_id`, `unit_id`, or `inst_id` → error
- Non-online entry missing `room_id` → error
- Online entry with non-empty `room_id` → warning
- Missing `affected_groups` for subgroup-lesson-merge → warning

### Timesheet mode
- Missing `database_ids` → error
- Regular session missing `database_ids.group`, `.unit`, `.instructor` → error
- Non-online missing `database_ids.room` → error
- Non-regular session missing `group_database`, `unit_database`, `inst_database`, `room_database` in `multiple_ids[]` → warning (missing fields) / error (room for non-online)

### Timetable mode
- `database_ids` present but incomplete → warning

---

## 6. Go Implementation Notes

### Input structs (from Go solver)
```go
type Session struct {
    Day          string            `json:"day"`
    Time         string            `json:"time"`
    Room         string            `json:"room"`
    Instructor   string            `json:"instructor"`
    Group        string            `json:"group"`
    Unit         string            `json:"unit"`
    Online       bool              `json:"online"`
    Type         string            `json:"type"`
    Multiple     bool              `json:"multiple"`
    MultipleIDs  []MultipleID      `json:"multiple_ids"`
    AffectedGroups []string        `json:"affected_groups"`
}

type MultipleID struct {
    GroupID      string `json:"group_id"`
    UnitID       string `json:"unit_id"`
    InstID       string `json:"inst_id"`
    RoomID       string `json:"room_id"`
    Online       bool   `json:"online"`
}
```

### EvaluationResult struct
```go
type EvaluationResult struct {
    HardScore       float64            `json:"hard_score"`       // 0-100
    PreferenceScore float64            `json:"preference_score"` // 0-100
    DistributionScore float64          `json:"distribution_score"` // 0-100
    DefaultRoomScore float64           `json:"default_room_score"` // 0-100
    OverallScore    float64            `json:"overall_score"`    // 0-100
    TotalClashes    int                `json:"total_clashes"`
    HiddenSessions  int                `json:"hidden_sessions"`
    Violations      []ConstraintBreakdown `json:"violations,omitempty"`
}

type ConstraintBreakdown struct {
    Type        string `json:"type"`        // "group_clash", "instructor_clash", "room_clash", "preference", "distribution", "default_room"
    Description string `json:"description"` // Human-readable description
    Penalty     float64 `json:"penalty"`    // Penalty contribution
}
```

### Parameter interface for GA/SA
```go
type EvaluationParams struct {
    Days        []string // valid day strings
    PeriodTimes []string // valid time strings
    Lessons     []Lesson // for distribution checking
    Preferences []Preference // entity preferences
}
```
