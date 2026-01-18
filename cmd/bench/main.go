package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"opentui-bench/internal/cache"
	"opentui-bench/internal/db"
	"opentui-bench/internal/runner"
	"opentui-bench/internal/web"
)

var dbPath string

func defaultDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "bench.db"
	}
	return filepath.Join(home, "insmo.com/opentui-bench", "bench.db")
}

func main() {
	rootCmd := &cobra.Command{
		Use:   "bench",
		Short: "OpenTUI benchmark tracker",
	}

	rootCmd.PersistentFlags().StringVar(&dbPath, "db", defaultDBPath(), "database path")

	rootCmd.AddCommand(recordCmd())
	rootCmd.AddCommand(listCmd())
	rootCmd.AddCommand(showCmd())
	rootCmd.AddCommand(compareCmd())
	rootCmd.AddCommand(trendCmd())
	rootCmd.AddCommand(deleteCmd())
	rootCmd.AddCommand(serveCmd())
	rootCmd.AddCommand(hasCommitCmd())
	rootCmd.AddCommand(latestCommitCmd())
	rootCmd.AddCommand(backfillCmd())
	rootCmd.AddCommand(flamegraphCmd())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func recordCmd() *cobra.Command {
	var cfg runner.RunConfig
	var profileStr string

	cmd := &cobra.Command{
		Use:   "record",
		Short: "Record benchmark results",
		RunE: func(cmd *cobra.Command, args []string) error {
			if cfg.RepoPath == "" {
				return fmt.Errorf("repo path required")
			}

			database, err := db.Open(dbPath)
			if err != nil {
				return err
			}
			defer func() {
				if err := database.Close(); err != nil {
					fmt.Fprintf(os.Stderr, "Error closing database: %v\n", err)
				}
			}()

			cfg.Profile = runner.ProfileMode(profileStr)
			switch cfg.Profile {
			case runner.ProfileNone, runner.ProfileCPU:
			default:
				return fmt.Errorf("invalid profile mode: %s", profileStr)
			}

			runID, err := runner.Run(cmd.Context(), database, cfg)
			if err != nil {
				return err
			}

			color.Green("Recorded run #%d", runID)
			return nil
		},
	}

	cmd.Flags().StringVar(&cfg.RepoPath, "repo", "", "path to opentui repo (required)")
	cmd.Flags().StringVar(&cfg.ZigOptimize, "optimize", "ReleaseFast", "zig optimization level")
	cmd.Flags().IntVar(&cfg.Samples, "samples", 1, "number of benchmark samples")
	cmd.Flags().StringVar(&cfg.Filter, "filter", "", "filter benchmarks by category")
	cmd.Flags().StringVar(&cfg.FilterBenchmark, "filter-bench", "", "filter benchmarks by name")
	cmd.Flags().StringVar(&cfg.Notes, "notes", "", "optional notes")
	cmd.Flags().StringVar(&cfg.MachineID, "machine", "", "machine identifier")
	cmd.Flags().StringVar(&profileStr, "profile", string(runner.ProfileNone), "profile mode (none, cpu)")
	cmd.Flags().IntVar(&cfg.PerfFreq, "perf-freq", 997, "perf sampling frequency")

	if err := cmd.MarkFlagRequired("repo"); err != nil {
		panic(err)
	}

	return cmd
}

func listCmd() *cobra.Command {
	var limit int
	var branch, since string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List recorded runs",
		RunE: func(cmd *cobra.Command, args []string) error {
			database, err := db.Open(dbPath)
			if err != nil {
				return err
			}
			defer func() {
				if err := database.Close(); err != nil {
					fmt.Fprintf(os.Stderr, "Error closing database: %v\n", err)
				}
			}()

			runs, err := database.ListRuns(limit, branch, since)
			if err != nil {
				return err
			}

			if len(runs) == 0 {
				fmt.Println("No runs found")
				return nil
			}

			cyan := color.New(color.FgCyan)
			dim := color.New(color.Faint)

			_, _ = cyan.Printf("%-6s %-10s %-12s %-20s %s\n", "ID", "Commit", "Branch", "Date", "Notes")
			_, _ = dim.Println(strings.Repeat("-", 70))

			for _, r := range runs {
				count, err := database.CountResultsForRun(r.ID)
				if err != nil {
					return err
				}
				notes := r.Notes
				if len(notes) > 30 {
					notes = notes[:27] + "..."
				}
				date := r.RunDate
				if len(date) > 19 {
					date = date[:19]
				}
				fmt.Printf("%-6d %-10s %-12s %-20s %s (%d benchmarks)\n",
					r.ID, r.CommitHash, r.Branch, date, notes, count)
			}

			return nil
		},
	}

	cmd.Flags().IntVar(&limit, "limit", 10, "max runs to show")
	cmd.Flags().StringVar(&branch, "branch", "", "filter by branch")
	cmd.Flags().StringVar(&since, "since", "", "filter runs since date (YYYY-MM-DD)")

	return cmd
}

func showCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show [run_id or commit]",
		Short: "Show details of a run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			database, err := db.Open(dbPath)
			if err != nil {
				return err
			}
			defer func() {
				if err := database.Close(); err != nil {
					fmt.Fprintf(os.Stderr, "Error closing database: %v\n", err)
				}
			}()

			var run *db.Run
			if id, err := strconv.ParseInt(args[0], 10, 64); err == nil {
				run, err = database.GetRun(id)
				if err != nil {
					return fmt.Errorf("run not found: %w", err)
				}
			} else {
				run, err = database.GetRunByCommit(args[0])
				if err != nil {
					return fmt.Errorf("run not found for commit: %w", err)
				}
			}

			cyan := color.New(color.FgCyan)
			dim := color.New(color.Faint)

			_, _ = cyan.Printf("Run #%d\n", run.ID)
			_, _ = dim.Println(strings.Repeat("-", 50))
			fmt.Printf("Commit:  %s\n", run.CommitHash)
			fmt.Printf("Message: %s\n", run.CommitMessage)
			fmt.Printf("Branch:  %s\n", run.Branch)
			fmt.Printf("Date:    %s\n", run.RunDate)
			if run.Notes != "" {
				fmt.Printf("Notes:   %s\n", run.Notes)
			}
			fmt.Println()

			results, err := database.GetResultsForRun(run.ID)
			if err != nil {
				return err
			}

			printResults(results)
			return nil
		},
	}

	return cmd
}

func compareCmd() *cobra.Command {
	var threshold float64
	var filter string

	cmd := &cobra.Command{
		Use:   "compare [commit1] [commit2]",
		Short: "Compare two runs",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			database, err := db.Open(dbPath)
			if err != nil {
				return err
			}
			defer func() {
				if err := database.Close(); err != nil {
					fmt.Fprintf(os.Stderr, "Error closing database: %v\n", err)
				}
			}()

			var run1, run2 *db.Run

			if len(args) == 1 {
				run2, err = database.GetLatestRun()
				if err != nil {
					return fmt.Errorf("no runs found: %w", err)
				}
				run1, err = database.GetRunByCommit(args[0])
				if err != nil {
					return fmt.Errorf("baseline not found: %w", err)
				}
			} else {
				run1, err = database.GetRunByCommit(args[0])
				if err != nil {
					return fmt.Errorf("run1 not found: %w", err)
				}
				run2, err = database.GetRunByCommit(args[1])
				if err != nil {
					return fmt.Errorf("run2 not found: %w", err)
				}
			}

			results1, err := database.GetResultsForRun(run1.ID)
			if err != nil {
				return err
			}
			results2, err := database.GetResultsForRun(run2.ID)
			if err != nil {
				return err
			}

			cyan := color.New(color.FgCyan)
			dim := color.New(color.Faint)
			red := color.New(color.FgRed)
			green := color.New(color.FgGreen)
			yellow := color.New(color.FgYellow)

			_, _ = cyan.Printf("Comparing %s vs %s\n", run1.CommitHash, run2.CommitHash)
			_, _ = dim.Printf("Baseline: %s (%s)\n", run1.CommitHash, shortDate(run1.RunDate))
			_, _ = dim.Printf("Current:  %s (%s)\n", run2.CommitHash, shortDate(run2.RunDate))
			_, _ = dim.Printf("Threshold: %.1f%%\n\n", threshold)

			type resultKey struct {
				Category string
				Name     string
			}
			results2Map := make(map[resultKey]db.Result)
			for _, r := range results2 {
				results2Map[resultKey{Category: r.Category, Name: r.Name}] = r
			}

			_, _ = cyan.Printf("%-50s %12s %12s %10s\n", "Benchmark", "Baseline", "Current", "Change")
			_, _ = dim.Println(strings.Repeat("-", 90))

			regressions := 0
			improvements := 0

			for _, r1 := range results1 {
				if filter != "" && !strings.Contains(strings.ToLower(r1.Name), strings.ToLower(filter)) {
					continue
				}

				r2, ok := results2Map[resultKey{Category: r1.Category, Name: r1.Name}]
				if !ok {
					continue
				}

				var change float64
				changeValid := false
				if r1.AvgNs > 0 {
					change = float64(r2.AvgNs-r1.AvgNs) / float64(r1.AvgNs) * 100
					changeValid = true
				}

				name := r1.Name
				if len(name) > 48 {
					name = name[:45] + "..."
				}

				fmt.Printf("%-50s %12s %12s ",
					name,
					formatDuration(r1.AvgNs),
					formatDuration(r2.AvgNs))

				if !changeValid {
					_, _ = yellow.Printf("n/a\n")
					continue
				}

				if change > threshold {
					_, _ = red.Printf("+%.1f%% REGRESSION\n", change)
					regressions++
				} else if change < -5 {
					_, _ = green.Printf("%.1f%%\n", change)
					improvements++
				} else {
					_, _ = yellow.Printf("%+.1f%%\n", change)
				}
			}

			_, _ = dim.Println(strings.Repeat("-", 90))
			fmt.Printf("\nSummary: %d regressions, %d improvements\n", regressions, improvements)

			if regressions > 0 {
				_, _ = red.Println("Performance regressions detected!")
				return nil
			}
			_, _ = green.Println("No regressions detected")
			return nil
		},
	}

	cmd.Flags().Float64Var(&threshold, "threshold", 10, "regression threshold percentage")
	cmd.Flags().StringVar(&filter, "filter", "", "filter benchmarks by name")

	return cmd
}

func trendCmd() *cobra.Command {
	var limit int

	cmd := &cobra.Command{
		Use:   "trend [benchmark_name]",
		Short: "Show performance trend over time",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			database, err := db.Open(dbPath)
			if err != nil {
				return err
			}
			defer func() {
				if err := database.Close(); err != nil {
					fmt.Fprintf(os.Stderr, "Error closing database: %v\n", err)
				}
			}()

			trends, err := database.GetTrend(args[0], limit)
			if err != nil {
				return err
			}

			if len(trends) == 0 {
				fmt.Printf("No results found matching '%s'\n", args[0])
				return nil
			}

			cyan := color.New(color.FgCyan)
			dim := color.New(color.Faint)

			_, _ = cyan.Printf("Trend for: %s\n\n", trends[0].Result.Name)
			_, _ = cyan.Printf("%-10s %-12s %12s %s\n", "Commit", "Date", "Avg", "Trend")
			_, _ = dim.Println(strings.Repeat("-", 60))

			var maxNs int64
			for _, t := range trends {
				if t.Result.AvgNs > maxNs {
					maxNs = t.Result.AvgNs
				}
			}

			for i := len(trends) - 1; i >= 0; i-- {
				t := trends[i]
				date := t.Run.RunDate
				if len(date) > 10 {
					date = date[:10]
				}

				barLen := 0
				if maxNs > 0 {
					barLen = int(float64(t.Result.AvgNs) / float64(maxNs) * 20)
				}
				if barLen > 20 {
					barLen = 20
				} else if barLen < 0 {
					barLen = 0
				}
				bar := strings.Repeat("█", barLen) + strings.Repeat("░", 20-barLen)

				fmt.Printf("%-10s %-12s %12s %s\n",
					t.Run.CommitHash,
					date,
					formatDuration(t.Result.AvgNs),
					bar)
			}

			return nil
		},
	}

	cmd.Flags().IntVar(&limit, "limit", 20, "max data points")

	return cmd
}

func deleteCmd() *cobra.Command {
	var before string

	cmd := &cobra.Command{
		Use:   "delete [run_id]",
		Short: "Delete runs",
		RunE: func(cmd *cobra.Command, args []string) error {
			database, err := db.Open(dbPath)
			if err != nil {
				return err
			}
			defer func() {
				if err := database.Close(); err != nil {
					fmt.Fprintf(os.Stderr, "Error closing database: %v\n", err)
				}
			}()

			if before != "" {

				count, err := database.DeleteRunsBefore(before)
				if err != nil {
					return err
				}
				color.Green("Deleted %d runs before %s", count, before)
				return nil
			}

			if len(args) == 0 {
				return fmt.Errorf("specify run_id or --before date")
			}

			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid run ID: %w", err)
			}

			if err := database.DeleteRun(id); err != nil {
				return err
			}

			color.Green("Deleted run #%d", id)
			return nil
		},
	}

	cmd.Flags().StringVar(&before, "before", "", "delete runs before date (YYYY-MM-DD)")

	return cmd
}

func serveCmd() *cobra.Command {
	var port int
	var open bool

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start web UI server",
		RunE: func(cmd *cobra.Command, args []string) error {
			database, err := db.Open(dbPath)
			if err != nil {
				return err
			}
			defer func() {
				if err := database.Close(); err != nil {
					fmt.Fprintf(os.Stderr, "Error closing database: %v\n", err)
				}
			}()

			addr := fmt.Sprintf(":%d", port)
			server := web.NewServer(database, addr)
			return server.Start(open)
		},
	}

	cmd.Flags().IntVar(&port, "port", 8080, "port to listen on")
	cmd.Flags().BoolVar(&open, "open", false, "open browser automatically")

	return cmd
}

func hasCommitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "has-commit [commit_hash_full]",
		Short: "Check if a commit has been recorded (exit 0 if exists, 1 if not)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			database, err := db.Open(dbPath)
			if err != nil {
				return err
			}
			defer func() {
				if err := database.Close(); err != nil {
					fmt.Fprintf(os.Stderr, "Error closing database: %v\n", err)
				}
			}()

			exists, err := database.HasCommit(args[0])
			if err != nil {
				return err
			}

			if exists {
				fmt.Printf("Commit %s already recorded\n", shortHash(args[0]))
				return nil
			}

			return fmt.Errorf("commit %s not found", shortHash(args[0]))
		},
	}

	return cmd
}

func latestCommitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "latest-commit",
		Short: "Print the most recently recorded commit hash (full)",
		RunE: func(cmd *cobra.Command, args []string) error {
			database, err := db.Open(dbPath)
			if err != nil {
				return err
			}
			defer func() {
				if err := database.Close(); err != nil {
					fmt.Fprintf(os.Stderr, "Error closing database: %v\n", err)
				}
			}()

			run, err := database.GetLatestRun()
			if err != nil {
				return fmt.Errorf("no recorded commits")
			}

			fmt.Println(run.CommitHashFull)
			return nil
		},
	}

	return cmd
}

func backfillCmd() *cobra.Command {
	var count int
	var start string
	var dryRun bool
	var flamegraph bool
	var cfg runner.RunConfig
	var profileStr string

	cmd := &cobra.Command{
		Use:   "backfill",
		Short: "Record benchmarks for N commits starting from a given commit",
		Long: `Backfill benchmarks for historical commits.

This command iterates through N commits starting from --start (default: HEAD),
running benchmarks for any that haven't been recorded yet.

Best run locally on stable hardware for consistent results.
The GitHub CI runner has variable performance that makes trends noisy.

Example:
  # Dry run to see which commits would be recorded (last 20 from HEAD)
  bench backfill --repo ~/insmo.com/opentui --count 20 --dry-run

  # Backfill 20 commits starting from a specific commit
  bench backfill --repo ~/insmo.com/opentui --start abc123 --count 20

  # Backfill with CPU profiles
  bench backfill --repo ~/insmo.com/opentui --count 20 --profile cpu`,
		RunE: func(cmd *cobra.Command, args []string) error {
			database, err := db.Open(dbPath)
			if err != nil {
				return err
			}
			defer func() {
				if err := database.Close(); err != nil {
					fmt.Fprintf(os.Stderr, "Error closing database: %v\n", err)
				}
			}()

			cfg.Profile = runner.ProfileMode(profileStr)
			switch cfg.Profile {
			case runner.ProfileNone, runner.ProfileCPU:
			default:
				return fmt.Errorf("invalid profile mode: %s", profileStr)
			}

			if flamegraph && cfg.Profile == runner.ProfileNone {
				cfg.Profile = runner.ProfileCPU
			}

			return runBackfill(cmd.Context(), database, count, start, dryRun, cfg)
		},
	}

	cmd.Flags().IntVar(&count, "count", 10, "number of commits to backfill")
	cmd.Flags().StringVar(&start, "start", "HEAD", "commit to start from")
	cmd.Flags().StringVar(&cfg.RepoPath, "repo", "", "path to opentui repo (required)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show which commits would be recorded without running benchmarks")
	cmd.Flags().StringVar(&cfg.Notes, "notes", "backfill", "notes to add to recorded runs")
	cmd.Flags().StringVar(&cfg.ZigOptimize, "optimize", "ReleaseFast", "zig optimization level")
	cmd.Flags().IntVar(&cfg.Samples, "samples", 3, "number of benchmark samples")
	cmd.Flags().StringVar(&cfg.Filter, "filter", "", "filter benchmarks by category")
	cmd.Flags().StringVar(&cfg.FilterBenchmark, "filter-bench", "", "filter benchmarks by name")
	cmd.Flags().StringVar(&cfg.MachineID, "machine", "", "machine identifier")
	cmd.Flags().BoolVar(&flamegraph, "flamegraph", false, "deprecated: use --profile cpu")
	cmd.Flags().StringVar(&profileStr, "profile", string(runner.ProfileNone), "profile mode (none, cpu)")
	cmd.Flags().IntVar(&cfg.PerfFreq, "perf-freq", 997, "perf sampling frequency")

	if err := cmd.MarkFlagRequired("repo"); err != nil {
		panic(err)
	}

	return cmd
}

func flamegraphCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "flamegraph",
		Short: "Manage CPU flamegraphs",
	}

	cmd.AddCommand(flamegraphListCmd())
	cmd.AddCommand(flamegraphSVGCmd())
	cmd.AddCommand(flamegraphStacksCmd())
	cmd.AddCommand(flamegraphDiffCmd())

	return cmd
}

func flamegraphListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list [commit]",
		Short: "List available flamegraphs for a run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			database, err := db.Open(dbPath)
			if err != nil {
				return err
			}
			defer func() {
				if err := database.Close(); err != nil {
					fmt.Fprintf(os.Stderr, "Error closing database: %v\n", err)
				}
			}()

			run, err := database.GetRunByCommit(args[0])
			if err != nil {
				return fmt.Errorf("run not found: %w", err)
			}

			benchmarks, err := database.ListFlamegraphBenchmarks(run.ID)
			if err != nil {
				return err
			}

			if len(benchmarks) == 0 {
				fmt.Printf("No flamegraphs recorded for %s\n", run.CommitHash)
				return nil
			}

			cyan := color.New(color.FgCyan)
			_, _ = cyan.Printf("Flamegraphs for %s (%d benchmarks):\n", run.CommitHash, len(benchmarks))
			for _, name := range benchmarks {
				fmt.Printf("  - %s\n", name)
			}
			return nil
		},
	}

	return cmd
}

func flamegraphSVGCmd() *cobra.Command {
	var outputFile string

	cmd := &cobra.Command{
		Use:   "svg [commit] [benchmark_name]",
		Short: "Output flamegraph SVG for a specific benchmark",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			database, err := db.Open(dbPath)
			if err != nil {
				return err
			}
			defer func() {
				if err := database.Close(); err != nil {
					fmt.Fprintf(os.Stderr, "Error closing database: %v\n", err)
				}
			}()

			run, err := database.GetRunByCommit(args[0])
			if err != nil {
				return fmt.Errorf("run not found: %w", err)
			}

			fg, err := database.GetFlamegraph(run.ID, args[1])
			if err != nil {
				return fmt.Errorf("flamegraph not found: %w", err)
			}

			svg, err := cache.GenerateSVG(fg.FoldedStacks, args[1])
			if err != nil {
				return fmt.Errorf("generate svg: %w", err)
			}

			if outputFile != "" {
				if err := os.WriteFile(outputFile, svg, 0o644); err != nil {
					return fmt.Errorf("write file: %w", err)
				}
				color.Green("Wrote %s (%d bytes)", outputFile, len(svg))
				return nil
			}

			_, _ = os.Stdout.Write(svg)
			return nil
		},
	}

	cmd.Flags().StringVarP(&outputFile, "output", "o", "", "output file (default: stdout)")

	return cmd
}

func flamegraphStacksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stacks [commit] [benchmark_name]",
		Short: "Output raw folded stacks for a specific benchmark",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			database, err := db.Open(dbPath)
			if err != nil {
				return err
			}
			defer func() {
				if err := database.Close(); err != nil {
					fmt.Fprintf(os.Stderr, "Error closing database: %v\n", err)
				}
			}()

			run, err := database.GetRunByCommit(args[0])
			if err != nil {
				return fmt.Errorf("run not found: %w", err)
			}

			fg, err := database.GetFlamegraph(run.ID, args[1])
			if err != nil {
				return fmt.Errorf("flamegraph not found: %w", err)
			}

			fmt.Print(fg.FoldedStacks)
			return nil
		},
	}

	return cmd
}

func flamegraphDiffCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "diff [commit1] [commit2] [benchmark_name]",
		Short: "Generate differential flamegraph between two commits for a benchmark",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			database, err := db.Open(dbPath)
			if err != nil {
				return err
			}
			defer func() {
				if err := database.Close(); err != nil {
					fmt.Fprintf(os.Stderr, "Error closing database: %v\n", err)
				}
			}()

			run1, err := database.GetRunByCommit(args[0])
			if err != nil {
				return fmt.Errorf("run1 not found: %w", err)
			}

			run2, err := database.GetRunByCommit(args[1])
			if err != nil {
				return fmt.Errorf("run2 not found: %w", err)
			}

			fg1, err := database.GetFlamegraph(run1.ID, args[2])
			if err != nil {
				return fmt.Errorf("flamegraph for %s not found: %w", args[0], err)
			}

			fg2, err := database.GetFlamegraph(run2.ID, args[2])
			if err != nil {
				return fmt.Errorf("flamegraph for %s not found: %w", args[1], err)
			}

			tmpDir, err := os.MkdirTemp("", "flamegraph-diff-")
			if err != nil {
				return err
			}
			defer func() { _ = os.RemoveAll(tmpDir) }()

			stacks1 := filepath.Join(tmpDir, "stacks1.folded")
			stacks2 := filepath.Join(tmpDir, "stacks2.folded")

			if err := os.WriteFile(stacks1, []byte(fg1.FoldedStacks), 0o644); err != nil {
				return err
			}
			if err := os.WriteFile(stacks2, []byte(fg2.FoldedStacks), 0o644); err != nil {
				return err
			}

			diffCmd := exec.Command("inferno-diff-folded", stacks1, stacks2)
			diffOut, err := diffCmd.Output()
			if err != nil {
				return fmt.Errorf("inferno-diff-folded: %w (is inferno installed?)", err)
			}

			fgCmd := exec.Command("inferno-flamegraph")
			fgCmd.Stdin = strings.NewReader(string(diffOut))
			svg, err := fgCmd.Output()
			if err != nil {
				return fmt.Errorf("inferno-flamegraph: %w", err)
			}

			_, _ = os.Stdout.Write(svg)
			return nil
		},
	}

	return cmd
}

type commitInfo struct {
	hash    string
	short   string
	message string
	date    string
}

func runBackfill(ctx context.Context, database *db.DB, count int, start string, dryRun bool, cfg runner.RunConfig) error {
	zigDir := filepath.Join(cfg.RepoPath, "packages/core/src/zig")

	if _, err := os.Stat(zigDir); os.IsNotExist(err) {
		return fmt.Errorf("zig directory not found: %s", zigDir)
	}

	out, err := runGitCommand(ctx, cfg.RepoPath, "log", "--reverse", fmt.Sprintf("-%d", count), start, "--format=%H|%h|%s|%cI")
	if err != nil {
		return fmt.Errorf("git log: %w", err)
	}

	var commits []commitInfo
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 4)
		if len(parts) < 4 {
			continue
		}
		commits = append(commits, commitInfo{
			hash:    parts[0],
			short:   parts[1],
			message: parts[2],
			date:    parts[3],
		})
	}

	var unrecorded []commitInfo
	for _, c := range commits {
		exists, err := database.HasCommit(c.hash)
		if err != nil {
			return fmt.Errorf("check commit %s: %w", c.short, err)
		}
		if !exists {
			unrecorded = append(unrecorded, c)
		}
	}

	if len(unrecorded) == 0 {
		color.Green("All %d commits already recorded", len(commits))
		return nil
	}

	cyan := color.New(color.FgCyan)
	yellow := color.New(color.FgYellow)

	_, _ = cyan.Printf("Found %d unrecorded commits (of %d checked):\n\n", len(unrecorded), len(commits))
	for i, c := range unrecorded {
		msg := c.message
		if len(msg) > 50 {
			msg = msg[:47] + "..."
		}
		fmt.Printf("  %d. %s %s\n", i+1, c.short, msg)
	}
	fmt.Println()

	if dryRun {
		_, _ = yellow.Println("Dry run - no benchmarks recorded")
		return nil
	}

	origHead, err := runGitCommand(ctx, cfg.RepoPath, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("get HEAD: %w", err)
	}
	origHead = strings.TrimSpace(origHead)

	status, _ := runGitCommand(ctx, cfg.RepoPath, "status", "--porcelain")
	if strings.TrimSpace(status) != "" {
		return fmt.Errorf("opentui repo has uncommitted changes, please commit or stash first")
	}

	for i, c := range unrecorded {
		_, _ = cyan.Printf("\n[%d/%d] Recording %s: %s\n", i+1, len(unrecorded), c.short, truncate(c.message, 50))

		if _, err := runGitCommand(ctx, cfg.RepoPath, "checkout", c.hash); err != nil {
			color.Red("  Failed to checkout: %v", err)
			continue
		}

		// Apply tooling patch (benchmarks live in simonklee/local-dev)
		out, err := runGitCommand(ctx, cfg.RepoPath, "cherry-pick", "simonklee/local-dev", "--no-commit")
		if err != nil {
			if strings.Contains(out, "previous cherry-pick is now empty") || strings.Contains(out, "nothing to commit") {
				_, _ = runGitCommand(ctx, cfg.RepoPath, "cherry-pick", "--abort")
			} else {
				color.Yellow("  Warning: cherry-pick failed: %v", err)
				_, _ = runGitCommand(ctx, cfg.RepoPath, "cherry-pick", "--abort")
				_, _ = runGitCommand(ctx, cfg.RepoPath, "reset", "--hard", "HEAD")
				continue
			}
		}

		runCfg := cfg
		runCfg.Notes = fmt.Sprintf("%s (commit %d/%d)", cfg.Notes, i+1, len(unrecorded))

		color.White("  Running benchmarks (v2)...")
		runID, err := runner.Run(ctx, database, runCfg)
		if err != nil {
			color.Red("  Failed: %v", err)
		} else {
			color.Green("  Done (Run #%d)", runID)
		}

		// Reset repo to clean state
		_, _ = runGitCommand(ctx, cfg.RepoPath, "reset", "--hard", "HEAD")
	}

	if _, err := runGitCommand(ctx, cfg.RepoPath, "checkout", origHead); err != nil {
		_, _ = yellow.Printf("\nWarning: failed to restore HEAD to %s: %v\n", shortHash(origHead), err)
	}

	color.Green("\nBackfill complete")
	return nil
}

func runGitCommand(ctx context.Context, repoPath string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoPath
	out, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))
	if err != nil {
		if output != "" {
			return string(out), fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, output)
		}
		return string(out), fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

func printResults(results []db.Result) {
	cyan := color.New(color.FgCyan)
	dim := color.New(color.Faint)

	var maxNameLen int
	for _, r := range results {
		if len(r.Name) > maxNameLen {
			maxNameLen = len(r.Name)
		}
	}
	if maxNameLen > 50 {
		maxNameLen = 50
	}

	_, _ = cyan.Printf("%-*s %12s %12s %12s\n", maxNameLen, "Benchmark", "Min", "Avg", "Max")
	_, _ = dim.Println(strings.Repeat("-", maxNameLen+40))

	currentCategory := ""
	for _, r := range results {
		if r.Category != currentCategory {
			currentCategory = r.Category
			_, _ = cyan.Printf("\n%s\n", currentCategory)
		}

		name := r.Name
		if len(name) > maxNameLen {
			name = name[:maxNameLen-3] + "..."
		}

		fmt.Printf("%-*s %12s %12s %12s\n",
			maxNameLen, name,
			formatDuration(r.MinNs),
			formatDuration(r.AvgNs),
			formatDuration(r.MaxNs))
	}
}

func formatDuration(ns int64) string {
	if ns < 1000 {
		return fmt.Sprintf("%dns", ns)
	} else if ns < 1_000_000 {
		return fmt.Sprintf("%.2fus", float64(ns)/1000)
	} else if ns < 1_000_000_000 {
		return fmt.Sprintf("%.2fms", float64(ns)/1_000_000)
	}
	return fmt.Sprintf("%.2fs", float64(ns)/1_000_000_000)
}

func shortDate(value string) string {
	if len(value) > 10 {
		return value[:10]
	}
	return value
}

func shortHash(value string) string {
	if len(value) <= 7 {
		return value
	}
	return value[:7]
}
