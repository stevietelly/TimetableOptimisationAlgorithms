package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"geliana-go/pkg/models"
	"geliana-go/pkg/progress"
	"geliana-go/pkg/solver"
	"os"
	"runtime"
	"strings"
	"time"
)

var globalStartTime time.Time

var humanOutput bool

func main() {
	globalStartTime = time.Now()
	inputPath := flag.String("i", "", "Input JSON file path (reads from stdin if empty)")
	outputPath := flag.String("o", "", "Output JSON file path (stdout if empty)")
	mapPath := flag.String("map", "", "Output search map path (.json or .html)")
	algo := flag.String("a", "genetic", "Algorithm to use (genetic, annealing, cp)")
	timeout := flag.Int("t", 30, "Timeout in seconds")
	verbose := flag.Bool("v", false, "Verbose output")
	human := flag.Bool("h", false, "Human-readable output (for terminal use)")

	// generic params
	optimiseDefualtRooms := flag.Bool("or", true, "Optimise for Default Rooms")
	optimiseRoomCapaciy := flag.Bool("oc", false, "Optimise for room Capacities")
	optimisePreferences := flag.Bool("op", true, "Optimise for Prefernces")

	// Genetic Solver Params
	popSize := flag.Int("pop", 50, "Genetic: Population size")
	maxGen := flag.Int("gen", 100, "Genetic: Max generations")
	mutRate := flag.Float64("mut", 0.1, "Genetic: Mutation rate (0.0-1.0)")

	// SA Solver Params
	maxIter := flag.Int("iter", 100000, "SA: Max iterations")
	initTemp := flag.Float64("temp", 1000.0, "SA: Initial temperature")
	coolRate := flag.Float64("cool", 0.995, "SA: Cooling rate")

	// CP Solver Params
	valueOrdering := flag.Int("vo", 0, "Constraint Satisfation: Value Ordering")
	variableInstantiation := flag.Int("vi", 0, "Constrainst Satisfaction: Variable Instantiation")

	// Heuristic CP variety params (0/empty = defaults: random seed, ties, 1 attempt)
	seedFlag := flag.Int64("seed", 0, "Heuristic CP: RNG seed (0 = random each run)")
	shuffleFlag := flag.String("shuffle", "", "Heuristic CP: candidate shuffling (off, ties, full)")
	restartsFlag := flag.Int("restarts", 0, "Heuristic CP: random-restart attempts, best wins (<=0 = single attempt)")

	flag.Parse()

	humanOutput = *human

	// 1. Read and Parse Input
	var req models.Request
	req.OptimiseDefaultRooms = *optimiseDefualtRooms
	req.OptimisePreferences = *optimisePreferences
	req.OptimiseRoomCapacity = *optimiseRoomCapaciy

	var rawInput []byte
	if *inputPath != "" {
		data, err := os.ReadFile(*inputPath)
		if err != nil {
			reportError("FILE_ERROR", fmt.Sprintf("Failed to read input file: %v", err))
			os.Exit(1)
		}
		rawInput = data
	} else {
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Buffer(make([]byte, 10*1024*1024), 10*1024*1024)
		if !scanner.Scan() {
			reportError("INVALID_INPUT", "No input received on stdin")
			os.Exit(1)
		}
		rawInput = []byte(scanner.Text())
	}
	if err := json.Unmarshal(rawInput, &req); err != nil {
		reportError("INVALID_INPUT", fmt.Sprintf("Failed to parse JSON: %v", err))
		os.Exit(1)
	}

	// The frontend (and legacy cloud) contract uses optimise_for_* keys.
	// When present they take precedence over the CLI flag defaults above.
	// The legacy misspelled key is still honoured for backward compatibility.
	var flagOverrides struct {
		OptimiseForPreferences  *bool `json:"optimise_for_preferences"`
		OptimiseForDefaultRooms *bool `json:"optimise_for_default_rooms"`
		OptimiseForRoomCapacity *bool `json:"optimise_for_room_capacity"`
		OptimisePreferences     *bool `json:"optimise_preferences"`
		OptimisePreferencesTypo *bool `json:"optimise_preferencess"`
		Seed                    *int64 `json:"seed"`
		Shuffle                  *string `json:"shuffle"`
		Restarts                  *int    `json:"restarts"`
	}
	if err := json.Unmarshal(rawInput, &flagOverrides); err == nil {
		if flagOverrides.OptimisePreferencesTypo != nil {
			req.OptimisePreferences = *flagOverrides.OptimisePreferencesTypo
		}
		if flagOverrides.OptimisePreferences != nil {
			req.OptimisePreferences = *flagOverrides.OptimisePreferences
		}
		if flagOverrides.OptimiseForPreferences != nil {
			req.OptimisePreferences = *flagOverrides.OptimiseForPreferences
		}
		if flagOverrides.OptimiseForDefaultRooms != nil {
			req.OptimiseDefaultRooms = *flagOverrides.OptimiseForDefaultRooms
		}
		if flagOverrides.OptimiseForRoomCapacity != nil {
			req.OptimiseRoomCapacity = *flagOverrides.OptimiseForRoomCapacity
		}
		if flagOverrides.Seed != nil {
			req.Seed = *flagOverrides.Seed
		}
		if flagOverrides.Shuffle != nil {
			req.Shuffle = *flagOverrides.Shuffle
		}
		if flagOverrides.Restarts != nil {
			req.Restarts = *flagOverrides.Restarts
		}
	}

	// Heuristic CP variety: CLI flags provide the base, the JSON payload wins
	// when present (same precedence shape as the optimise_for_* keys above).
	if req.Seed == 0 && *seedFlag != 0 {
		req.Seed = *seedFlag
	}
	if req.Shuffle == "" && *shuffleFlag != "" {
		req.Shuffle = *shuffleFlag
	}
	if req.Restarts <= 0 && *restartsFlag > 0 {
		req.Restarts = *restartsFlag
	}

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

	case "lazy":
		s = solver.NewLazySolver()
	case "heuristic_cp":

		s = solver.NewHeuristicCPSolver(solver.VariableOrderMode(*variableInstantiation), solver.ValueOrderMode(*valueOrdering))
	default:
		ga := solver.NewGeneticSolver(*popSize, *maxGen)
		ga.MutationRate = *mutRate
		s = ga
	}

	tracker := progress.NewDefaultTracker(0, func(report solver.ProgressReport) {
		if *human {
			if report.StepLabel == "config" {
				return
			}
			metrics := report.Metrics
			if *verbose {
				line, _ := json.MarshalIndent(metrics, "", "  ")
				fmt.Fprintf(os.Stderr, "\r[%s] %s | step %d/%d | fitness %.2f | ETA %.1fs | %s    ",
					*algo, report.StepLabel, report.CurrentStep, report.TotalSteps,
					report.BestFitness, report.ETASeconds, string(line))
			}
			return
		}

		line, _ := json.Marshal(map[string]interface{}{
			"type":         "progress",
			"progress":     float64(report.CurrentStep) / float64(max(report.TotalSteps, 1)),
			"message":      report.StepLabel,
			"current_step": report.CurrentStep,
			"total_steps":  report.TotalSteps,
			"best_fitness": report.BestFitness,
			"eta_seconds":  report.ETASeconds,
			"metrics":      report.Metrics,
		})
		fmt.Println(string(line))

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
			_ = os.WriteFile(*mapPath, []byte(html), 0644)
		} else {
			traceData, _ := json.MarshalIndent(trace, "", "  ")
			_ = os.WriteFile(*mapPath, traceData, 0644)
		}
	}

	// Check if solution is valid
	if !result.Stats.SolutionFound {
		reportError("INVALID_SOLUTION", "Could not find a valid clash-free solution within the limits")
		os.Exit(4)
	}

	// 5. Write Output
	if *human {
		fmt.Println()
		fmt.Printf("┌─ Solver Configuration ─────────────────────────────\n")
		fmt.Printf("│ Algorithm     : %s\n", *algo)
		if *algo == "heuristic_cp" {
			fmt.Printf("│ Variable Order: %s\n", solver.VariableOrderMode(*variableInstantiation))
			fmt.Printf("│ Value Order   : %s\n", solver.ValueOrderMode(*valueOrdering))
		}
		fmt.Printf("│ Input file    : %s\n", *inputPath)
		fmt.Printf("└────────────────────────────────────────────────────\n")

		fmt.Println()
		fmt.Printf("┌─ Solution Stats ───────────────────────────────────\n")
		fmt.Printf("│ Overall score      : %.2f\n", result.Stats.OverallScore)
		fmt.Printf("│ Hard score         : %.2f\n", result.Stats.HardScore)
		fmt.Printf("│ Preference score   : %.2f\n", result.Stats.PreferenceScore)
		fmt.Printf("│ Distribution score : %.2f\n", result.Stats.DistributionScore)
		fmt.Printf("│ Default room score : %.2f\n", result.Stats.DefaultRoomScore)
		fmt.Printf("│ Total clashes      : %d\n", result.Stats.TotalClashes)
		fmt.Printf("│ Hidden sessions    : %d\n", result.Stats.HiddenSessions)
		fmt.Printf("│ Health             : %s\n", result.Stats.Health)
		fmt.Printf("│ Time taken         : %.2fs\n", result.Stats.TimeTaken)
		fmt.Printf("│ Memory used        : %.2f MB\n", result.Stats.MemoryUsageMB)
		fmt.Printf("└────────────────────────────────────────────────────\n")
		fmt.Println()
		fmt.Printf("Message: %s\n", result.Message)
		if sess, ok := result.Sessions.([]models.Session); ok {
			fmt.Printf("Sessions scheduled: %d\n", len(sess))
		}
	} else {
		resultLine, _ := json.Marshal(map[string]interface{}{
			"type":  "result",
			"value": result,
		})
		fmt.Println(string(resultLine))
	}

	if *outputPath != "" {
		outputData, _ := json.MarshalIndent(result, "", "  ")
		if err := os.WriteFile(*outputPath, outputData, 0644); err != nil {
			reportError("WRITE_ERROR", fmt.Sprintf("Failed to write output: %v", err))
			os.Exit(1)
		}
	}

	if *verbose || *human {
		fmt.Fprintf(os.Stderr, "\n✅ Optimization complete in %.2fs! Memory used: %.2f MB Timetable Score %.2f\n", result.Stats.TimeTaken, result.Stats.MemoryUsageMB, result.Stats.OverallScore)
	}
}

func validateFeasibility(req *models.Request) error {
	totalSlots := len(req.Days) * len(req.Blocks)
	if totalSlots == 0 {
		return fmt.Errorf("no schedule slots available (check days/blocks)")
	}

	blocks := len(req.Blocks)
	if blocks == 0 {
		return fmt.Errorf("Blocks dont match with times")
	}

	if len(req.Lessons) == 0 {
		return fmt.Errorf("no lessons available")
	}

	total_elements := len(req.Groups) * len(req.Instructors) * len(req.Units)

	if total_elements == 0 {
		return fmt.Errorf(" missing elements (check units, groups, instructors )")
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

	if humanOutput {
		fmt.Fprintf(os.Stderr, "Error [%s]: %s\n", code, message)
		return
	}

	errorLine, _ := json.Marshal(map[string]interface{}{
		"type":    "error",
		"message": message,
		"code":    code,
		"value":   resp,
	})
	fmt.Fprintln(os.Stderr, string(errorLine))
	fmt.Println(string(errorLine))
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

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
