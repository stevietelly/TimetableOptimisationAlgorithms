package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"geliana-go/pkg/models"
	"geliana-go/pkg/progress"
	"geliana-go/pkg/solver"
	"io/ioutil"
	"os"
	"runtime"
	"strings"
	"time"
)

var globalStartTime time.Time

func main() {
	globalStartTime = time.Now()
	inputPath := flag.String("i", "", "Input JSON file path")
	outputPath := flag.String("o", "output.json", "Output JSON file path")
	mapPath := flag.String("map", "", "Output search map path (.json or .html)")
	algo := flag.String("a", "genetic", "Algorithm to use (genetic, annealing, cp)")
	timeout := flag.Int("t", 30, "Timeout in seconds")
	verbose := flag.Bool("v", false, "Verbose output")

	// Genetic Solver Params
	popSize := flag.Int("pop", 50, "Genetic: Population size")
	maxGen := flag.Int("gen", 100, "Genetic: Max generations")
	mutRate := flag.Float64("mut", 0.1, "Genetic: Mutation rate (0.0-1.0)")

	// SA Solver Params
	maxIter := flag.Int("iter", 100000, "SA: Max iterations")
	initTemp := flag.Float64("temp", 1000.0, "SA: Initial temperature")
	coolRate := flag.Float64("cool", 0.995, "SA: Cooling rate")

	flag.Parse()

	if *inputPath == "" {
		fmt.Println("Error: input file (-i) is required")
		os.Exit(1)
	}

	// 1. Read and Parse Input
	data, err := ioutil.ReadFile(*inputPath)
	if err != nil {
		reportError("FILE_ERROR", fmt.Sprintf("Failed to read input file: %v", err))
		os.Exit(1)
	}

	var req models.Request
	if err := json.Unmarshal(data, &req); err != nil {
		reportError("INVALID_INPUT", fmt.Sprintf("Failed to parse JSON: %v", err))
		os.Exit(1)
	}

	// Normalize data (handle nested configuration)
	req.Normalize()

	// 2. Feasibility Check
	if err := validateFeasibility(&req); err != nil {
		reportError("INFEASIBLE", err.Error())
		os.Exit(2)
	}

	// 3. Setup Solver & Tracker
	var s solver.Solver
	switch *algo {
	case "annealing":
		sa := solver.NewSimulatedAnnealingSolver(*maxIter)
		sa.InitialTemp = *initTemp
		sa.CoolingRate = *coolRate
		s = sa
	case "cp":
		s = solver.NewCPSolver()
	default:
		ga := solver.NewGeneticSolver(*popSize, *maxGen)
		ga.MutationRate = *mutRate
		s = ga
	}

	tracker := progress.NewDefaultTracker(0, func(report solver.ProgressReport) {
		if *verbose {
			fmt.Fprintf(os.Stderr, "\r[%s] Step: %d/%d | Fitness: %.2f | ETA: %.1fs    ",
				*algo, report.CurrentStep, report.TotalSteps, report.BestFitness, report.ETASeconds)
		}
	})

	// 4. Run Optimization with Timeout
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeout)*time.Second)
	defer cancel()

	result, err := s.Solve(ctx, &req, tracker)
	if err != nil {
		if err == context.DeadlineExceeded {
			reportError("TIMEOUT", "Optimization exceeded time limit")
			os.Exit(3)
		}
		reportError("SOLVER_ERROR", err.Error())
		os.Exit(1)
	}

	// Capture memory stats
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	result.Stats.MemoryUsageMB = float64(m.Alloc) / 1024 / 1024

	// Collect Trace if requested
	if *mapPath != "" {
		trace := tracker.GetTrace()
		result.Trace = trace
		
		if strings.HasSuffix(*mapPath, ".html") {
			html := generateHTMLTrace(trace, *algo)
			_ = ioutil.WriteFile(*mapPath, []byte(html), 0644)
		} else {
			traceData, _ := json.MarshalIndent(trace, "", "  ")
			_ = ioutil.WriteFile(*mapPath, traceData, 0644)
		}
	}

	// Check if solution is valid (no hard clashes)
	if !result.Stats.SolutionFound {
		reportError("INVALID_SOLUTION", "Could not find a valid clash-free solution within the limits")
		os.Exit(4)
	}

	// 5. Write Output
	outputData, _ := json.MarshalIndent(result, "", "  ")
	if err := ioutil.WriteFile(*outputPath, outputData, 0644); err != nil {
		reportError("WRITE_ERROR", fmt.Sprintf("Failed to write output: %v", err))
		os.Exit(1)
	}

	if *verbose {
		fmt.Printf("\n✅ Optimization complete! Memory used: %.2f MB\n", result.Stats.MemoryUsageMB)
	}
}

func validateFeasibility(req *models.Request) error {
	totalSlots := len(req.Days) * len(req.Blocks)
	if totalSlots == 0 {
		return fmt.Errorf("no schedule slots available (check days/blocks)")
	}

	totalLessons := 0
	for _, l := range req.Lessons {
		for _, count := range l.Distribution {
			totalLessons += count
		}
	}

	if totalLessons > totalSlots*len(req.Rooms) && len(req.Rooms) > 0 {
		return fmt.Errorf("too many lessons (%d) for available room-slots (%d)", totalLessons, totalSlots*len(req.Rooms))
	}

	return nil
}

func reportError(code, message string) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	resp := models.Response{
		Error:   true,
		Message: message,
		Stats: models.OptimizationStats{
			FailReason:    fmt.Sprintf("[%s] %s", code, message),
			MemoryUsageMB: float64(m.Alloc) / 1024 / 1024,
			TimeTaken:     time.Since(globalStartTime).Seconds(),
		},
	}
	data, _ := json.MarshalIndent(resp, "", "  ")
	fmt.Fprintln(os.Stderr, string(data))
}

func generateHTMLTrace(trace []models.TraceStep, algo string) string {
	var rows strings.Builder
	for _, t := range trace {
		rows.WriteString(fmt.Sprintf(`<tr><td>%d</td><td>%s</td><td>%s</td><td>%.2f</td><td>%.4fs</td></tr>`,
			t.Step, t.Type, t.Label, t.Score, t.Timestamp))
	}

	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
	<title>Geliana Solver Trace - %s</title>
	<style>
		body { font-family: sans-serif; margin: 20px; background: #f4f4f9; }
		table { border-collapse: collapse; width: 100%%; background: white; box-shadow: 0 2px 5px rgba(0,0,0,0.1); }
		th, td { border: 1px solid #ddd; padding: 12px; text-align: left; }
		th { background-color: #007bff; color: white; }
		tr:nth-child(even) { background-color: #f2f2f2; }
		.header { margin-bottom: 20px; }
		h1 { color: #333; }
	</style>
</head>
<body>
	<div class="header">
		<h1>Search Space Map (%s)</h1>
		<p>Total Steps: %d</p>
	</div>
	<table>
		<thead>
			<tr>
				<th>Step</th>
				<th>Type</th>
				<th>Action/Label</th>
				<th>Score/Fitness</th>
				<th>Timestamp</th>
			</tr>
		</thead>
		<tbody>
			%s
		</tbody>
	</table>
</body>
</html>`, algo, algo, len(trace), rows.String())
}
