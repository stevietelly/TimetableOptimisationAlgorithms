# Geliana Go Optimization Engine

A high-performance, pure Go implementation of the Geliana timetable optimization algorithms. Designed specifically for low-latency execution and seamless deployment in resource-constrained environments like AWS Lambda or low end computers/laptops.

## Overview

The Go Engine is a dependency-free rewrite of the core Geliana optimization logic. It focuses on speed, memory efficiency, and strict constraint satisfaction.

### Key Features
- **Pure Go implementation**: No external C++ libraries (like OR-Tools), ensuring small binary size and simple deployment.
- **Lambda Optimized**: Built-in support for `context.Context` timeouts and structured JSON error reporting.
- **Multi-Core Parallelism**: The Genetic Solver utilizes goroutines to parallelize fitness evaluations.
- **Real-time Visibility**: Provides progress updates with linear ETA extrapolation.
- **Backward Compatibility**: Includes a legacy adapter to support older dataset formats (units/groups).
- **Resource Monitoring**: Reports precise memory usage (MB) and execution time for every run.

## Algorithms

| Algorithm | Type | Best For | Key Heuristics |
| :--- | :--- | :--- | :--- |
| **Genetic** (`-a genetic`) | Stochastic / Parallel | Large, complex schedules | Tournament selection, Elite survival |
| **Simulated Annealing** (`-a annealing`) | Stochastic / Iterative | Fast, "good enough" results | Intelligent neighbor generation |
| **Constraint Satisfaction** (`-a cp`) | Systematic / Complete | Guaranteed validity | Backtracking with MRV (Fail-First) |

## Getting Started

### Build
```bash
cd go-engine
go build -o geliana ./cmd/geliana
```

### Run
```bash
./geliana -i ./Data/lesson_based_sample.json -v -a genetic -t 60
```

### Flags
- `-i`: Path to the input JSON file (Required).
- `-o`: Path to the output JSON file (Default: `output.json`).
- `-a`: Algorithm to use: `genetic`, `annealing`, or `cp` (Default: `genetic`).
- `-t`: Timeout in seconds (Default: `30`).
- `-v`: Enable verbose output (progress reporting and ETA).

## Input Format

The engine exclusively uses the **Lesson-Based Format**. If a legacy format (units/groups) is provided, the engine automatically normalizes it during the pre-processing phase.

See `PAYLOAD_FORMAT_SPECIFICATION.md` in the root directory for the full schema.

## Output Format

The engine returns a structured JSON object containing:
- `error`: Boolean indicating if the process failed.
- `message`: Human-readable status or error message.
- `sessions`: Array of scheduled lessons with space-time coordinates and database IDs.
- `stats`:
    - `overall_score`: Quality of the solution (0-100).
    - `time_taken`: Execution time in seconds.
    - `memory_usage_mb`: Peak heap memory allocation.
    - `solution_found`: Boolean indicating if a valid (clash-free) solution was achieved.
