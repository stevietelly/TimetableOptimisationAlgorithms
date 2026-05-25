# Agent Guidelines for Geliana Go Engine

This document provides specialized instructions for AI agents maintaining or extending the Go-based optimization engine.

## Core Architecture

- **`pkg/models`**: Contains the Go structs mirroring the JSON payload.
    - **Mandate**: Use `json` tags for all fields.
    - **Normalization**: The `Normalize()` method handles structural variations and triggers the `LegacyToLessonBased` adapter.
- **`pkg/solver`**: The core optimization logic.
    - **`solver.go`**: Defines the `Solver` interface. All new algorithms MUST implement `Solve(ctx, req, tracker)`.
    - **Context Awareness**: Solvers MUST check `ctx.Done()` in their main loops to ensure they respect Lambda timeouts.
- **`pkg/progress`**: Utilities for tracking state and calculating ETAs.
- **`cmd/geliana`**: CLI and Lambda wrapper. Handles I/O, error reporting, and resource monitoring.

## Development Standards

### 1. Error Handling
- Use structured JSON error reporting via `reportError` in `main.go`.
- Return specific error codes (e.g., `INFEASIBLE`, `TIMEOUT`, `INVALID_INPUT`) to assist caller logic.

### 2. Complex Lesson Mandates
- **Subgroup Synchronization**: Any new algorithm or mutation MUST enforce that all `SchedulableItem`s sharing the same `LessonID` and `DistIdx` (subgroups) remain locked to the same Day and Block.
- **Output Expansion**: Output transformations MUST expand `lesson-merge` and `subgroup` internal combinations into the `multiple_ids` array. Do not return flat sessions for complex types.
- **Online Tagging**: Ensure the `online` boolean is propagated to both the session root and every `multiple_ids` entry.

### 3. Performance & Concurrency
- Prefer goroutines for embarrassingly parallel tasks like GA fitness calculation.
- Avoid global state; encapsulate solver parameters within their respective structs.
- Use `runtime.ReadMemStats` for memory tracking.

### 3. Preferences & Constraints
- Always implement new constraints in both the `calculateFitness` (GA/SA) and `isConsistent` (CP) methods.
- Support the structured `Preference` object format. Legacy DSL strings should be converted at the data layer if possible.

## Testing Procedures

### Verification
- **Compilation**: Ensure `go build ./cmd/geliana` succeeds without warnings.
- **Validity**: Run `./geliana -i <file> -a cp` to verify that a "valid" solution can be found for known good datasets.
- **Lambda Readiness**: Test with very short timeouts (`-t 1`) to verify graceful early-exit and structured error output.

### Benchmarking
- Measure performance using the `Green Park High School.json` dataset.
- Compare memory usage reported in `stats` against previous versions to prevent resource regressions.

## Adding a New Algorithm
1. Create a new file in `pkg/solver/`.
2. Implement the `Solver` interface.
3. Register the algorithm in the `switch` statement in `cmd/geliana/main.go`.
4. Add the algorithm to the documentation table in `go-engine/README.md`.
