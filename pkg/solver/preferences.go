package solver

import (
	"geliana-go/pkg/models"
	"strings"
)

// ---------------------------------------------------------------------------
// PREFERENCE EVALUATION -- read this comment block before touching anything
// below it. It records what the two source documents (Preferences.md and
// the TS reference evaluator) actually say, where they DISAGREE with each
// other, and which side this file picked and why. If preference scoring
// ever looks wrong, this is the first place to check.
// ---------------------------------------------------------------------------
//
// WHAT'S IMPLEMENTED HERE, AND WHY THIS SUBSET:
//
//   Kinds:  DAY, TIME, ROOM
//   Types:  BEFORE, AFTER, EXCEPT, ONLY, ALL
//
// This is a deliberately NARROWER set than Preferences.md's own "currently
// supported objects" list (UNIT, TIME, DAYTIME, DAY, ROOM, GROUP). It was
// narrowed down to DAY/TIME/ROOM because THREE independent sources agree on
// exactly that set and nothing more:
//
//   1. models.PreferenceTarget's own doc comment: `Kind string // "DAY",
//      "TIME", "ROOM"` -- the Go struct's author already scoped it to these
//      three, regardless of what the markdown doc separately claims.
//   2. The TS reference evaluator's isPreferenceViolated() switch statement
//      only has working `if (kind === ...)` branches for DAY, TIME, ROOM
//      (plus PERIOD/BREAK -- see below). There is no `kind === "UNIT"` or
//      `kind === "GROUP"` branch anywhere in it -- a preference with one of
//      those kinds would silently fall through to `return null` (never
//      violated, no matter what), which is almost certainly not what anyone
//      intended when they wrote {EXCEPT [UNIT:->'1']} into a preference.
//   3. Every worked example in Preferences.md's own table uses only
//      DAY/TIME/ROOM as the kind -- EXCEPT for the "only unit 1" row, which
//      is ALSO the one row that doesn't match its own explanation (it's
//      encoded as EXCEPT, not ONLY, for a claimed "only unit 1" rule).
//
// Net effect: UNIT/DAYTIME/GROUP-kind preferences are documented but not
// actually functional anywhere in the system today. Rather than inventing
// semantics for them here with nothing to check them against, they're left
// unimplemented -- HasKnownKind() below returns false for them, and callers
// should treat that as "skip, don't guess" rather than "violated" or
// "satisfied" (both would be a lie).
//
// PERIOD/BREAK kinds exist in the TS switch but are excluded here too: the
// TS file has a `// TODO: Probable bug here` comment on its own BEFORE/PERIOD
// handling, and the AFTER/BREAK and AFTER/PERIOD branches both start with an
// unconditional `break;` BEFORE their real logic -- meaning that logic is
// unreachable dead code in the reference implementation itself. There's
// nothing working to port.
//
// MULTI-VALUE ONLY/EXCEPT (the part Preferences.md documents but the TS
// evaluator doesn't actually check): models.Preference already has plural
// Days/Times/RoomIDs fields sitting right next to the singular Target.Value
// -- these aren't used by the TS isPreferenceViolated as given, but they
// exist for exactly the "ONLY at 2pm, 3pm, and 8am" case the markdown
// describes. This file checks the plural field FIRST when it's non-empty,
// and falls back to the singular Target.Value otherwise -- so a preference
// built the old single-value way still behaves exactly like the TS version,
// and one built with the plural fields gets real multi-value support.
//
// AND / OR conjunctions: "AND" needs no special code here at all -- per
// Preferences.md, AND just means "both rules must hold", which is exactly
// what you get for free by putting two separate Preference entries in an
// entity's Preferences slice and requiring each to individually pass (which
// is how EvaluatePreferences-style callers already work: iterate the slice,
// flag each independent violation). "OR" is explicitly marked deprecated in
// the markdown and has no corresponding logic in the TS file either, so it's
// not implemented here.
//
// ---------------------------------------------------------------------------

// Preference type strings, exactly as they appear on the wire in
// models.Preference.Type. Kept as untyped string consts (not a custom type)
// so they compare directly against the plain-string field without casts.
const (
	PrefBefore = "BEFORE"
	PrefAfter  = "AFTER"
	PrefExcept = "EXCEPT"
	PrefOnly   = "ONLY"
	PrefAll    = "ALL"
)

// Preference kind strings, exactly as they appear on the wire in
// models.PreferenceTarget.Kind.
const (
	KindDay  = "DAY"
	KindTime = "TIME"
	KindRoom = "ROOM"
)

// HasKnownKind reports whether this file knows how to evaluate the given
// preference's kind at all. See the package comment above for exactly which
// kinds are documented-but-unimplemented (UNIT, DAYTIME, GROUP) and why.
// Callers should skip unknown-kind preferences rather than guessing at a
// violated/satisfied answer for them.
func HasKnownKind(pref models.Preference) bool {
	switch pref.Target.Kind {
	case KindDay, KindTime, KindRoom:
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// Candidate: the "proposed placement" a preference gets checked against.
// Deliberately plain data (no pointer back into a solver's internal state)
// so this whole file works the same whether it's called from
// HeuristicCPSolver mid-search, from a one-off script, or from a test --
// that's what "in a separate file where it can be accessed by others" means
// in practice: nothing in here reaches back into HeuristicCPSolver at all.
// ---------------------------------------------------------------------------

type Candidate struct {
	DayName string   // e.g. "Monday" -- must match an entry in the Days slice passed to violated()
	Time    string   // e.g. "2:00pm" -- same string format used throughout Block.Start
	RoomIDs []string // every room this candidate placement would use (0 for online, 1 for regular, N for lesson-merge/subgroup)
}

// Violated reports whether this single preference is broken by this single
// candidate placement. Mirrors the TS reference's isPreferenceViolated()
// structure directly: switch on Type (BEFORE/AFTER/EXCEPT/ONLY/ALL), then
// branch on Kind (DAY/TIME/ROOM) inside each. Unknown kinds are treated as
// "not violated" -- see HasKnownKind if you need to distinguish "passed the
// check" from "there was no check to run".
//
// days is the schedule's ordered day list (e.g. ["Monday", ..., "Friday"]),
// needed for BEFORE/AFTER on DAY, which are inherently POSITIONAL ("before
// Wednesday" means "earlier in the week", not any kind of string ordering).
func Violated(pref models.Preference, days []string, c Candidate) bool {
	if !HasKnownKind(pref) {
		return false // nothing to check -- see package comment
	}

	kind := pref.Target.Kind
	value := pref.Target.Value

	switch pref.Type {

	case PrefAll:
		// "All values present can be used" -- this is a statement that
		// nothing is restricted, not a rule to enforce. Always passes.
		return false

	case PrefOnly:
		switch kind {
		case KindDay:
			allowed := pref.Days
			if len(allowed) == 0 && value != "" {
				allowed = []string{value} // fall back to the single-value form
			}
			return !containsFold(allowed, c.DayName)

		case KindTime:
			allowed := pref.Times
			if len(allowed) == 0 && value != "" {
				allowed = []string{value}
			}
			return !timeInList(c.Time, allowed)

		case KindRoom:
			allowed := pref.RoomIDs
			if len(allowed) == 0 && value != "" {
				allowed = []string{value}
			}
			// ONLY-room is violated if ANY room this candidate uses falls
			// outside the allowed list. A single disallowed room among
			// several (lesson-merge/subgroup) is still a violation --
			// "only room A15" doesn't become satisfied just because ONE of
			// four rooms happens to be A15.
			for _, r := range c.RoomIDs {
				if !containsExact(allowed, r) {
					return true
				}
			}
			return false
		}

	case PrefExcept:
		switch kind {
		case KindDay:
			excluded := pref.Days
			if len(excluded) == 0 && value != "" {
				excluded = []string{value}
			}
			return containsFold(excluded, c.DayName)

		case KindTime:
			excluded := pref.Times
			if len(excluded) == 0 && value != "" {
				excluded = []string{value}
			}
			return timeInList(c.Time, excluded)

		case KindRoom:
			excluded := pref.RoomIDs
			if len(excluded) == 0 && value != "" {
				excluded = []string{value}
			}
			for _, r := range c.RoomIDs {
				if containsExact(excluded, r) {
					return true
				}
			}
			return false
		}

	case PrefBefore:
		switch kind {
		case KindDay:
			// Positional, not alphabetical: "before Wednesday" means earlier
			// in the week's own order, matching TS's this.days.indexOf(...).
			sessionIdx := indexOfFold(days, c.DayName)
			targetIdx := indexOfFold(days, value)
			if sessionIdx == -1 || targetIdx == -1 {
				return false // day not in the schedule at all -- nothing sane to compare
			}
			return sessionIdx >= targetIdx // same-day counts as NOT before, matching TS's >=

		case KindTime:
			return timeToMinutes(c.Time) >= timeToMinutes(value) // same-time also fails BEFORE, matching TS
		}

	case PrefAfter:
		switch kind {
		case KindDay:
			sessionIdx := indexOfFold(days, c.DayName)
			targetIdx := indexOfFold(days, value)
			if sessionIdx == -1 || targetIdx == -1 {
				return false
			}
			return sessionIdx <= targetIdx // same-day also fails AFTER, matching TS

		case KindTime:
			return timeToMinutes(c.Time) < timeToMinutes(value)
		}
	}

	return false
}

// ---------------------------------------------------------------------------
// Small string-list helpers. Go doesn't have a built-in "does this slice
// contain this string, case-insensitively" -- these exist so the Violated
// switch above stays readable instead of repeating strings.EqualFold loops
// inline everywhere.
// ---------------------------------------------------------------------------

func containsFold(list []string, target string) bool {
	for _, v := range list {
		if strings.EqualFold(v, target) {
			return true
		}
	}
	return false
}

func containsExact(list []string, target string) bool {
	for _, v := range list {
		if v == target {
			return true
		}
	}
	return false
}

func indexOfFold(list []string, target string) int {
	for i, v := range list {
		if strings.EqualFold(v, target) {
			return i
		}
	}
	return -1
}

// timeInList compares by MINUTES, not raw string equality -- "2:00pm" and
// "2:00PM" (or any other formatting quirk) should count as the same time.
// A plain string-equality check here would silently under-match on any
// casing/spacing difference between how a preference was authored and how
// the schedule's own block Start strings are formatted.
func timeInList(t string, list []string) bool {
	tm := timeToMinutes(t)
	for _, v := range list {
		if timeToMinutes(v) == tm {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Gathering WHICH preferences apply to a container -- mirrors the TS
// reference's EvaluatePreferences() entity-gathering exactly, one branch per
// lesson type, since each type pulls preferences from a different mix of
// group/instructor/unit entities:
//
//   regular:      one unit + one group + one instructor
//   subgroup:     one group (shared) + each division's OWN instructor + unit
//   lesson-merge: each affected group + one shared instructor + one shared unit
//   subgroup-lesson-merge: each combo's group + instructor + unit
// ---------------------------------------------------------------------------

// RelevantPreferences returns every preference that could apply to this
// container, pulled from whichever groups/instructors/units it actually
// involves. Pass in your own ID-keyed lookup maps (HeuristicCPSolver already
// builds groupByID/unitByID for the default-room feature -- add an
// instructorByID the same way to use this).
func RelevantPreferences(
	item *Container,
	groupByID map[string]models.Group,
	unitByID map[string]models.Unit,
	instructorByID map[string]models.Instructor,
) []models.Preference {
	var prefs []models.Preference

	switch item.LessonDef.Type {

	case "subgroup":
		if g, ok := groupByID[item.Group]; ok {
			prefs = append(prefs, g.Preferences...)
		}
		for _, m := range item.LessonDef.MultipleIDs {
			if inst, ok := instructorByID[m.InstID]; ok {
				prefs = append(prefs, inst.Preferences...)
			}
			if u, ok := unitByID[m.UnitID]; ok {
				prefs = append(prefs, u.Preferences...)
			}
		}

	case "lesson-merge":
		for _, m := range item.LessonDef.MultipleIDs {
			if g, ok := groupByID[m.GroupID]; ok {
				prefs = append(prefs, g.Preferences...)
			}
		}
		if inst, ok := instructorByID[item.Instructor]; ok {
			prefs = append(prefs, inst.Preferences...)
		}
		if u, ok := unitByID[item.Unit]; ok {
			prefs = append(prefs, u.Preferences...)
		}

	case "subgroup-lesson-merge":
		for _, m := range item.LessonDef.MultipleIDs {
			if g, ok := groupByID[m.GroupID]; ok {
				prefs = append(prefs, g.Preferences...)
			}
			if inst, ok := instructorByID[m.InstID]; ok {
				prefs = append(prefs, inst.Preferences...)
			}
			if u, ok := unitByID[m.UnitID]; ok {
				prefs = append(prefs, u.Preferences...)
			}
		}

	default: // "regular"
		if u, ok := unitByID[item.Unit]; ok {
			prefs = append(prefs, u.Preferences...)
		}
		if g, ok := groupByID[item.Group]; ok {
			prefs = append(prefs, g.Preferences...)
		}
		if inst, ok := instructorByID[item.Instructor]; ok {
			prefs = append(prefs, inst.Preferences...)
		}
	}

	return prefs
}

// ViolationCount runs every relevant preference against one candidate
// placement and counts how many fail. This is the number a value-ordering
// heuristic wants: lower is better, same shape as domainSize/load in
// heuristic_cp.go, so it slots into orderedDays (or an equivalent room/time
// ordering) the same way -- see the wiring note in heuristic_cp.go.
func ViolationCount(prefs []models.Preference, days []string, c Candidate) int {
	count := 0
	for _, p := range prefs {
		if Violated(p, days, c) {
			count++
		}
	}
	return count
}