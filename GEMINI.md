# Geliana Go Optimization Engine (GEMINI.md)

This project is a high-performance, dependency-free Go implementation of the Geliana timetable optimization engine, designed for deployment in resource-constrained environments like AWS Lambda.

## Project Overview

- **Core Goal**: Assign lessons to day/time/room slots while maximizing constraint satisfaction.
- **Language**: Go 1.26.2
- **Architecture**:
    - `cmd/geliana/`: Entry point, CLI/Lambda wrapper, I/O handling, and resource monitoring.
    - `pkg/models/`: Core data structures and normalization (including legacy format support).
    - `pkg/solver/`: Optimization algorithms (Genetic, Simulated Annealing, CP).
    - `pkg/evaluator/`: Weighted scoring engine (Hard, Soft, Distribution, Rooms).
    - `pkg/progress/`: Real-time progress tracking and ETA calculation.

## Building and Running

### Commands
- **Build**: `go build -o geliana ./cmd/geliana`
- **Run**: `./geliana -i <input.json> -a <algo> -t <timeout_secs>`
- **Test**: `go test ./...` (TODO: Verify existing test coverage)

### CLI Flags
- `-i`: Input JSON path (required or stdin).
- `-o`: Output JSON path.
- `-a`: Algorithm: `genetic` (default), `annealing`, `cp`.
- `-t`: Timeout in seconds (default: 30).
- `-v`: Verbose output (stderr progress reporting).
- `-map`: Output a search map trace (JSON or HTML).

## Core Algorithms

### 1. Genetic Solver (`-a genetic`)
A robust two-phase stochastic solver.
- **Phase 1**: Focuses exclusively on repairing hard clashes (Group, Instructor, Room). For **subgroups**, it enforces slot-synchronization using interval-based overlap detection.
- **Phase 2**: Optimizes soft constraints (Preferences, Distribution). Multi-entity lessons (merges) are scored as a single unit but validated against all student group schedules.
- **Concurrency**: Parallelizes fitness evaluation across all CPU cores using goroutines.

### 2. Simulated Annealing (`-a annealing`)
A fast stochastic solver using a cooling schedule to escape local optima. Best for "good enough" results in very large search spaces.

### 3. Constraint Satisfaction (`-a cp`)
(Implementation detail: Systematic backtracking with MRV heuristic). Best for guaranteed valid solutions in smaller, highly constrained problems.

## Scoring Engine (Evaluator)

Total Score = `(Hard * 0.60) + (Preferences * 0.20) + (Distribution * 0.10) + (DefaultRoom * 0.10)`

| Category | Weight | Description |
| :--- | :--- | :--- |
| **Hard** | 60% | No overlaps for Groups, Instructors, or Rooms. Correctly handles `AffectedGroups` and `AffectedInstructors` for complex lessons. |
| **Preferences** | 20% | Respects `ONLY`, `EXCEPT`, `BEFORE`, `AFTER` for all involved resources in a session. |
| **Distribution** | 10% | Matches the desired number of lessons per block/day. |
| **Default Room**| 10% | Prefers assigned rooms. In merges, multiple rooms may be assigned via comma-separated identifiers. |

## Advanced Data Handling
- **Subgroup Sync**: The engine guarantees that all subdivisions of a subgroup lesson (e.g., Physics, French, and Art electives) are scheduled at the exact same time and day.
- **Expansion Protocol**: The solver automatically expands `lesson-merge` definitions into full `multiple_ids` arrays in the output, ensuring the frontend can display every student group involved.
- **Online Persistence**: The `online: true` status is preserved at both the session level and for every individual resource combination.

## Development Conventions

### 1. Data Integrity & Normalization
- All models must use `json` tags.
- The `models.Request.Normalize()` method must be called before solving to handle configuration fallbacks and legacy data conversion.

### 2. Error Handling
- Use structured JSON error reporting via the `reportError` function in `main.go`.
- Standard error codes: `FILE_ERROR`, `INVALID_INPUT`, `INFEASIBLE`, `TIMEOUT`, `SOLVER_ERROR`, `INVALID_SOLUTION`.

### 3. Performance & Resource Management
- **Zero Dependencies**: Maintain a pure Go codebase to ensure minimal binary size and fast startup.
- **Context Awareness**: All solvers must respect `ctx.Done()` to allow graceful termination under Lambda timeouts.
- **Memory Monitoring**: Use `runtime.ReadMemStats` to report peak heap usage in the final stats.

### 4. Progress Reporting
- Solvers report progress via the `ProgressTracker` interface.
- Progress is emitted as JSON lines to `stdout` for real-time monitoring by the caller.

## Key Files to Reference
- `pkg/models/models.go`: Core schema and normalization logic.
- `pkg/solver/genetic.go`: The primary optimization logic.
- `pkg/evaluator/evaluator.go`: The definitive source of truth for scoring and constraints.
- `Constraints.md`: Human-readable documentation of the scoring components.
- `Agents.md`: Specialized instructions for AI-driven maintenance.
