package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"opentui-bench/internal/backtest"
)

func backtestCmd() *cobra.Command {
	var branch string
	var window int
	var minPoints int
	var baselineOffset int
	var alpha float64
	var fdr float64
	var minRetainedOffShiftSignal float64
	var nearShiftWindow int
	var postShiftWindow int
	var knownShiftsRaw string
	var dfModesRaw string
	var floorsRaw string
	var cpPoliciesRaw string
	var jsonOutput string

	cmd := &cobra.Command{
		Use:   "backtest",
		Short: "Replay historical regression alerts across config sweeps",
		Long: `Replay all historical runs under a configuration grid and compute objective scorecards.

Metrics include alert volume, post-shift burstiness, alert persistence/decay,
category concentration, and fraction of alerts near known global-shift events.
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			database, cleanup, err := openDB()
			if err != nil {
				return err
			}
			defer cleanup()

			dfModes := parseCSVItems(dfModesRaw)
			if len(dfModes) == 0 {
				return fmt.Errorf("--df-modes must include at least one value")
			}

			floors, err := parseCSVFloat64s(floorsRaw)
			if err != nil {
				return fmt.Errorf("parse --absolute-floors: %w", err)
			}

			cpPolicies, err := parseCSVPolicies(cpPoliciesRaw)
			if err != nil {
				return fmt.Errorf("parse --cp-policies: %w", err)
			}

			knownShifts, err := parseCSVInt64s(knownShiftsRaw)
			if err != nil {
				return fmt.Errorf("parse --known-shifts: %w", err)
			}

			configs, err := backtest.BuildConfigGrid(dfModes, floors, cpPolicies)
			if err != nil {
				return err
			}

			opts := backtest.DefaultOptions()
			opts.Branch = branch
			opts.Window = window
			opts.MinPoints = minPoints
			opts.BaselineOffset = baselineOffset
			opts.Alpha = alpha
			opts.FDR = fdr
			opts.MinRetainedOffShiftSignal = minRetainedOffShiftSignal
			opts.NearShiftWindow = nearShiftWindow
			opts.PostShiftWindow = postShiftWindow
			opts.KnownShiftRunIDs = knownShifts

			report, err := backtest.Run(database, opts, configs)
			if err != nil {
				return err
			}

			printModelCard(report)

			if jsonOutput != "" {
				payload, err := json.MarshalIndent(report, "", "  ")
				if err != nil {
					return err
				}
				if err := os.WriteFile(jsonOutput, payload, 0o644); err != nil {
					return err
				}
				fmt.Printf("\nWrote backtest report: %s\n", jsonOutput)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&branch, "branch", "main", "branch to replay")
	cmd.Flags().IntVar(&window, "window", 30, "comparable-runs window")
	cmd.Flags().IntVar(&minPoints, "min-points", 5, "minimum baseline points")
	cmd.Flags().IntVar(&baselineOffset, "baseline-offset", 3, "runs skipped before baseline construction")
	cmd.Flags().Float64Var(&alpha, "alpha", 0.01, "per-hypothesis alpha for t-test path")
	cmd.Flags().Float64Var(&fdr, "fdr", 0.01, "Benjamini-Hochberg FDR threshold")
	cmd.Flags().Float64Var(&minRetainedOffShiftSignal, "min-retained-off-shift-signal", 0.25, "minimum retained off-shift signal ratio (0-1) required for recommendation eligibility")
	cmd.Flags().IntVar(&nearShiftWindow, "near-shift-window", 20, "runs after a shift counted as near-shift")
	cmd.Flags().IntVar(&postShiftWindow, "post-shift-window", 25, "runs after a shift used for burst/decay metrics")
	cmd.Flags().StringVar(&knownShiftsRaw, "known-shifts", "", "optional comma-separated shift run IDs (default: auto-detect)")
	cmd.Flags().StringVar(&dfModesRaw, "df-modes", "baseline,latest", "comma-separated df modes")
	cmd.Flags().StringVar(&floorsRaw, "absolute-floors", "0,1000,5000", "comma-separated absolute floors in ns")
	cmd.Flags().StringVar(&cpPoliciesRaw, "cp-policies", "off,attribution-only,recent-trigger", "comma-separated change-point policies")
	cmd.Flags().StringVar(&jsonOutput, "json", "", "optional path to write JSON output")

	return cmd
}

func printModelCard(report *backtest.Report) {
	fmt.Printf("Replay Backtest Model Card\n")
	fmt.Printf("Runs: %d | Configs: %d\n", len(report.RunIDs), len(report.ConfigResults))
	fmt.Printf("Known shift runs: %s\n", formatRunIDs(report.ShiftRunIDs))
	fmt.Printf("Near-shift window: %d | Post-shift window: %d\n", report.Options.NearShiftWindow, report.Options.PostShiftWindow)
	fmt.Printf("Min retained off-shift signal for recommendation: %.0f%%\n\n", report.Options.MinRetainedOffShiftSignal*100.0)

	fmt.Printf("%-4s %-9s %-7s %-17s %-6s %-6s %-6s %7s %10s %8s %8s %8s %11s %12s %8s\n",
		"Rank", "DF", "Floor", "CP Policy", "Best", "Curr", "Elig", "Retain", "Alerts/Run", "Burst", "Decay", "CatHHI", "NearShift%", "OffShift/Run", "Score")

	for i, result := range report.ConfigResults {
		best := ""
		if result.Recommended {
			best = "yes"
		}
		curr := ""
		if isCurrentDefault(result.Config) {
			curr = "yes"
		}
		elig := "no"
		if result.EligibleForRecommendation {
			elig = "yes"
		}
		fmt.Printf("%-4d %-9s %-7.0f %-17s %-6s %-6s %-6s %6.1f%% %10.2f %8.2f %8.2f %8.3f %10.1f%% %12.2f %8.3f\n",
			i+1,
			result.Config.DFMode,
			result.Config.MinAbsoluteNs,
			result.Config.ChangePointPolicy,
			best,
			curr,
			elig,
			result.Scorecard.RetainedOffShiftSignal*100.0,
			result.Scorecard.AlertsPerRun,
			result.Scorecard.BurstinessAfterShift,
			result.Scorecard.PersistenceAfterShift,
			result.Scorecard.CategoryHHI,
			result.Scorecard.NearShiftAlertFraction*100.0,
			result.Scorecard.OffShiftAlertsPerRun,
			result.ObjectiveScore,
		)
	}

	recommended := findRecommended(report.ConfigResults)
	if recommended != nil {
		fmt.Printf("\nRecommended defaults from scorecard: df=%s, absolute_floor=%.0fns, cp_policy=%s\n",
			recommended.Config.DFMode,
			recommended.Config.MinAbsoluteNs,
			recommended.Config.ChangePointPolicy,
		)
	}
}

func findRecommended(results []backtest.ConfigResult) *backtest.ConfigResult {
	for i := range results {
		if results[i].Recommended {
			return &results[i]
		}
	}
	return nil
}

func isCurrentDefault(cfg backtest.Config) bool {
	return cfg.DFMode == "baseline" && cfg.MinAbsoluteNs == 5000 && cfg.ChangePointPolicy == backtest.ChangePointPolicyRecentTrigger
}

func formatRunIDs(ids []int64) string {
	if len(ids) == 0 {
		return "(none)"
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, ",")
}

func parseCSVItems(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	parts := strings.Split(trimmed, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		value := strings.TrimSpace(part)
		if value == "" {
			continue
		}
		out = append(out, value)
	}
	return out
}

func parseCSVFloat64s(raw string) ([]float64, error) {
	items := parseCSVItems(raw)
	if len(items) == 0 {
		return nil, fmt.Errorf("no values provided")
	}
	out := make([]float64, 0, len(items))
	for _, item := range items {
		value, err := strconv.ParseFloat(item, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid float %q", item)
		}
		out = append(out, value)
	}
	return out, nil
}

func parseCSVInt64s(raw string) ([]int64, error) {
	items := parseCSVItems(raw)
	if len(items) == 0 {
		return nil, nil
	}
	out := make([]int64, 0, len(items))
	for _, item := range items {
		value, err := strconv.ParseInt(item, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid int64 %q", item)
		}
		out = append(out, value)
	}
	return out, nil
}

func parseCSVPolicies(raw string) ([]backtest.ChangePointPolicy, error) {
	items := parseCSVItems(raw)
	if len(items) == 0 {
		return nil, fmt.Errorf("no values provided")
	}
	out := make([]backtest.ChangePointPolicy, 0, len(items))
	for _, item := range items {
		policy, err := backtest.ParseChangePointPolicy(item)
		if err != nil {
			return nil, err
		}
		out = append(out, policy)
	}
	return out, nil
}
