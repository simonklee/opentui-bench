package backtest

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"opentui-bench/internal/db"
	"opentui-bench/internal/stats"
)

type Config struct {
	MinAbsoluteNs float64 `json:"min_absolute_ns"`
}

func (c Config) Label() string {
	return fmt.Sprintf("floor=%.0f", c.MinAbsoluteNs)
}

type Options struct {
	Branch                    string  `json:"branch"`
	Window                    int     `json:"window"`
	MinPoints                 int     `json:"min_points"`
	BaselineOffset            int     `json:"baseline_offset"`
	FDR                       float64 `json:"fdr"`
	MinRetainedOffShiftSignal float64 `json:"min_retained_off_shift_signal"`
	GlobalShiftMinBench       int     `json:"global_shift_min_benchmarks"`
	GlobalShiftMinShare       float64 `json:"global_shift_min_positive_share"`
	GlobalShiftMinGeoPct      float64 `json:"global_shift_min_geo_increase_pct"`

	NearShiftWindow  int     `json:"near_shift_window"`
	PostShiftWindow  int     `json:"post_shift_window"`
	KnownShiftRunIDs []int64 `json:"known_shift_run_ids,omitempty"`
}

type Scorecard struct {
	TotalRuns              int     `json:"total_runs"`
	AnalyzableRuns         int     `json:"analyzable_runs"`
	ShiftEvents            int     `json:"shift_events"`
	TotalAlerts            int     `json:"total_alerts"`
	NearShiftAlerts        int     `json:"near_shift_alerts"`
	OffShiftAlerts         int     `json:"off_shift_alerts"`
	AlertsPerRun           float64 `json:"alerts_per_run"`
	BurstinessAfterShift   float64 `json:"burstiness_after_shift_peak"`
	PersistenceAfterShift  float64 `json:"persistence_after_shift_runs"`
	CategoryTopShare       float64 `json:"category_top_share"`
	CategoryHHI            float64 `json:"category_hhi"`
	NearShiftAlertFraction float64 `json:"near_shift_alert_fraction"`
	OffShiftAlertsPerRun   float64 `json:"off_shift_alerts_per_run"`
	RetainedOffShiftSignal float64 `json:"retained_off_shift_signal"`
}

type ConfigResult struct {
	Config                    Config    `json:"config"`
	Scorecard                 Scorecard `json:"scorecard"`
	ObjectiveScore            float64   `json:"objective_score"`
	EligibleForRecommendation bool      `json:"eligible_for_recommendation"`
	Recommended               bool      `json:"recommended"`
}

type Report struct {
	GeneratedAt   string         `json:"generated_at"`
	Options       Options        `json:"options"`
	RunIDs        []int64        `json:"run_ids"`
	ShiftRunIDs   []int64        `json:"shift_run_ids"`
	ConfigResults []ConfigResult `json:"config_results"`
}

type shiftMetrics struct {
	detected bool
	compared int
	share    float64
	geoPct   float64
}

type benchmarkContext struct {
	Category     string
	Target       stats.OrderedRunStat
	Observations []stats.OrderedRunStat
}

type runContext struct {
	RunID                int64
	AnalyzableBenchmarks int
	GlobalShiftDetected  bool
	Benchmarks           []benchmarkContext
}

type runOutcome struct {
	RunID       int64
	Analyzable  bool
	AlertCount  int
	CategoryMap map[string]int
}

func DefaultOptions() Options {
	return Options{
		Branch:                    "main",
		Window:                    30,
		MinPoints:                 5,
		BaselineOffset:            3,
		FDR:                       0.01,
		MinRetainedOffShiftSignal: 0.25,
		GlobalShiftMinBench:       50,
		GlobalShiftMinShare:       0.75,
		GlobalShiftMinGeoPct:      10.0,
		NearShiftWindow:           20,
		PostShiftWindow:           25,
	}
}

func DefaultConfigs() []Config {
	floors := []float64{0, 1000, 5000}
	configs, _ := BuildConfigGrid(floors)
	return configs
}

func BuildConfigGrid(floors []float64) ([]Config, error) {
	if len(floors) == 0 {
		return nil, fmt.Errorf("at least one absolute floor is required")
	}

	configs := make([]Config, 0, len(floors))
	seen := make(map[string]struct{})
	for _, floor := range floors {
		if floor < 0 {
			return nil, fmt.Errorf("absolute floor must be >= 0 (got %.2f)", floor)
		}
		cfg := Config{MinAbsoluteNs: floor}
		key := cfg.Label()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		configs = append(configs, cfg)
	}

	return configs, nil
}

func Run(database *db.DB, opts Options, configs []Config) (*Report, error) {
	opts = normalizeOptions(opts)
	if len(configs) == 0 {
		configs = DefaultConfigs()
	}

	replayRunIDs, err := listReplayRunIDs(database, opts.Branch)
	if err != nil {
		return nil, fmt.Errorf("list replay runs: %w", err)
	}

	report := &Report{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Options:     opts,
		RunIDs:      replayRunIDs,
	}

	if len(replayRunIDs) == 0 {
		return report, nil
	}

	shiftRunIDs := uniqueSortedRunIDs(opts.KnownShiftRunIDs)
	if len(shiftRunIDs) == 0 {
		shiftRunIDs, err = detectKnownShiftRuns(database, replayRunIDs, opts)
		if err != nil {
			return nil, fmt.Errorf("detect known shifts: %w", err)
		}
	}
	report.ShiftRunIDs = filterKnownShiftRuns(shiftRunIDs, replayRunIDs)

	contexts := make([]runContext, len(replayRunIDs))
	for i, runID := range replayRunIDs {
		ctx, err := buildRunContext(database, runID, opts)
		if err != nil {
			return nil, fmt.Errorf("build context for run %d: %w", runID, err)
		}
		contexts[i] = ctx
	}

	results := make([]ConfigResult, 0, len(configs))
	for _, cfg := range configs {
		outcomes := make([]runOutcome, len(contexts))
		for i, ctx := range contexts {
			outcomes[i] = evaluateConfigForRun(ctx, cfg, opts)
		}
		score := computeScorecard(replayRunIDs, outcomes, report.ShiftRunIDs, opts)
		results = append(results, ConfigResult{Config: cfg, Scorecard: score})
	}

	assignObjectiveScores(results)
	applyRetentionConstraint(results, opts.MinRetainedOffShiftSignal)
	bestIndex := bestResultIndex(results)
	if bestIndex >= 0 {
		results[bestIndex].Recommended = true
	}

	sort.SliceStable(results, func(i, j int) bool {
		if results[i].EligibleForRecommendation != results[j].EligibleForRecommendation {
			return results[i].EligibleForRecommendation && !results[j].EligibleForRecommendation
		}
		if results[i].ObjectiveScore == results[j].ObjectiveScore {
			return results[i].Config.Label() < results[j].Config.Label()
		}
		return results[i].ObjectiveScore < results[j].ObjectiveScore
	})

	report.ConfigResults = results
	return report, nil
}

func normalizeOptions(opts Options) Options {
	defaults := DefaultOptions()
	if strings.TrimSpace(opts.Branch) == "" {
		opts.Branch = defaults.Branch
	}
	if opts.Window <= 0 {
		opts.Window = defaults.Window
	}
	if opts.MinPoints <= 0 {
		opts.MinPoints = defaults.MinPoints
	} else if opts.MinPoints < 2 {
		opts.MinPoints = 2
	}
	if opts.BaselineOffset < 0 {
		opts.BaselineOffset = defaults.BaselineOffset
	}
	if opts.FDR <= 0 {
		opts.FDR = defaults.FDR
	}
	if opts.MinRetainedOffShiftSignal < 0 {
		opts.MinRetainedOffShiftSignal = defaults.MinRetainedOffShiftSignal
	}
	if opts.MinRetainedOffShiftSignal > 1 {
		opts.MinRetainedOffShiftSignal = 1
	}
	if opts.GlobalShiftMinBench <= 0 {
		opts.GlobalShiftMinBench = defaults.GlobalShiftMinBench
	}
	if opts.GlobalShiftMinShare <= 0 {
		opts.GlobalShiftMinShare = defaults.GlobalShiftMinShare
	}
	if opts.GlobalShiftMinGeoPct <= 0 {
		opts.GlobalShiftMinGeoPct = defaults.GlobalShiftMinGeoPct
	}
	if opts.NearShiftWindow < 0 {
		opts.NearShiftWindow = defaults.NearShiftWindow
	}
	if opts.PostShiftWindow < 0 {
		opts.PostShiftWindow = defaults.PostShiftWindow
	}
	opts.KnownShiftRunIDs = uniqueSortedRunIDs(opts.KnownShiftRunIDs)
	return opts
}

func listReplayRunIDs(database *db.DB, branch string) ([]int64, error) {
	query := "SELECT id FROM runs WHERE branch = ? ORDER BY julianday(run_date) ASC, id ASC"
	args := []interface{}{branch}
	if branch == "main" {
		query = "SELECT id FROM runs WHERE branch = 'main' OR branch IS NULL OR branch = '' ORDER BY julianday(run_date) ASC, id ASC"
		args = nil
	}

	rows, err := database.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

func detectKnownShiftRuns(database *db.DB, replayRunIDs []int64, opts Options) ([]int64, error) {
	resultsCache := make(map[int64][]db.Result)
	getResults := func(runID int64) ([]db.Result, error) {
		if cached, ok := resultsCache[runID]; ok {
			return cached, nil
		}
		fetched, err := database.GetResultsForRun(runID)
		if err != nil {
			return nil, err
		}
		resultsCache[runID] = fetched
		return fetched, nil
	}

	var shiftRuns []int64
	for _, runID := range replayRunIDs {
		runs, err := comparableRunsForTarget(database, runID, 2, 0)
		if err != nil {
			return nil, err
		}
		if len(runs) < 2 {
			continue
		}
		metrics, err := computeShiftMetrics(getResults, runs[0].ID, runs[1].ID, opts)
		if err != nil {
			return nil, err
		}
		if metrics.detected {
			shiftRuns = append(shiftRuns, runs[0].ID)
		}
	}

	return uniqueSortedRunIDs(shiftRuns), nil
}

func buildRunContext(database *db.DB, runID int64, opts Options) (runContext, error) {
	ctx := runContext{RunID: runID}

	runs, err := comparableRunsForTarget(database, runID, opts.Window, opts.BaselineOffset)
	if err != nil {
		return ctx, err
	}
	if len(runs) == 0 {
		return ctx, nil
	}

	runIDs := make([]int64, len(runs))
	for i, run := range runs {
		runIDs[i] = run.ID
	}

	benchmarkKeys, err := database.GetDistinctBenchmarkKeys(runIDs)
	if err != nil {
		return ctx, err
	}

	latestRunID := runID
	resultsCache := make(map[int64][]db.Result)
	getResults := func(id int64) ([]db.Result, error) {
		if cached, ok := resultsCache[id]; ok {
			return cached, nil
		}
		fetched, err := database.GetResultsForRun(id)
		if err != nil {
			return nil, err
		}
		resultsCache[id] = fetched
		return fetched, nil
	}

	if len(runs) >= 2 {
		metrics, err := computeShiftMetrics(getResults, runs[0].ID, runs[1].ID, opts)
		if err != nil {
			return ctx, err
		}
		ctx.GlobalShiftDetected = metrics.detected
	}
	for _, benchmarkKey := range benchmarkKeys {
		resultsMap, err := database.GetResultsForBenchmarkInRuns(benchmarkKey, runIDs)
		if err != nil {
			return ctx, err
		}

		latestResult, hasLatest := resultsMap[latestRunID]
		if !hasLatest {
			continue
		}
		if latestResult.AvgNs <= 0 {
			continue
		}
		benchmarkTrends, err := database.GetTrend(latestResult.ID, opts.Window+opts.BaselineOffset+1)
		if err != nil {
			return ctx, err
		}

		var observations []stats.OrderedRunStat
		var targetObservation stats.OrderedRunStat
		for _, trend := range benchmarkTrends {
			run := trend.Run
			result := trend.Result
			runDate, err := time.Parse(time.RFC3339Nano, run.RunDate)
			if err != nil {
				return ctx, fmt.Errorf("parse run %d date: %w", run.ID, err)
			}
			observation := stats.OrderedRunStat{RunDate: runDate, Stat: resultToRunStat(run.ID, result)}
			observations = append(observations, observation)
			if run.ID == latestRunID {
				targetObservation = observation
			}
		}

		evaluation := stats.EvaluateSnapshot(targetObservation, observations, stats.SnapshotConfig{
			Window: opts.Window, MinPoints: opts.MinPoints, BaselineOffset: opts.BaselineOffset,
		})
		if evaluation.Baseline == nil {
			continue
		}

		ctx.Benchmarks = append(ctx.Benchmarks, benchmarkContext{
			Category:     latestResult.Category,
			Target:       targetObservation,
			Observations: observations,
		})
	}

	ctx.AnalyzableBenchmarks = len(ctx.Benchmarks)
	return ctx, nil
}

func comparableRunsForTarget(database *db.DB, runID int64, window int, baselineOffset int) ([]db.Run, error) {
	target, err := database.GetRun(runID)
	if err != nil {
		return nil, err
	}
	if target.Branch == "" || target.Branch == "main" {
		return database.GetComparableRunsWindow(runID, window+baselineOffset+1)
	}

	historyWindow := window + baselineOffset
	mainRuns, err := database.GetComparableMainRunsWindow(runID, historyWindow)
	if err != nil {
		return nil, err
	}
	return append([]db.Run{*target}, mainRuns...), nil
}

func evaluateConfigForRun(ctx runContext, cfg Config, opts Options) runOutcome {
	outcome := runOutcome{
		RunID:       ctx.RunID,
		Analyzable:  ctx.AnalyzableBenchmarks > 0,
		CategoryMap: make(map[string]int),
	}
	if !outcome.Analyzable {
		return outcome
	}

	type benchEval struct {
		category string
		testOK   bool
	}

	pValues := make([]float64, len(ctx.Benchmarks))
	evals := make([]benchEval, len(ctx.Benchmarks))

	for i, bench := range ctx.Benchmarks {
		evaluation := stats.EvaluateSnapshot(bench.Target, bench.Observations, stats.SnapshotConfig{
			Window: opts.Window, MinPoints: opts.MinPoints, BaselineOffset: opts.BaselineOffset,
		})
		if evaluation.Baseline == nil {
			continue
		}
		testResult := evaluation.Result
		testOK := *testResult.ChangePercent >= stats.MinPracticalRegressionEffectPercent &&
			*testResult.AbsoluteChangeNs >= cfg.MinAbsoluteNs
		evals[i] = benchEval{category: bench.Category, testOK: testOK}
		pValues[i] = *testResult.PValue
	}

	for _, result := range stats.BenjaminiHochberg(pValues, opts.FDR) {
		if result.IsSignificant && evals[result.Index].testOK {
			outcome.AlertCount++
			outcome.CategoryMap[evals[result.Index].category]++
		}
	}

	return outcome
}

func computeScorecard(runIDs []int64, outcomes []runOutcome, shiftRunIDs []int64, opts Options) Scorecard {
	score := Scorecard{TotalRuns: len(runIDs)}
	if len(runIDs) == 0 || len(outcomes) == 0 {
		return score
	}

	indexByRunID := make(map[int64]int, len(runIDs))
	for i, runID := range runIDs {
		indexByRunID[runID] = i
	}

	nearMask := make([]bool, len(runIDs))
	for _, shiftRunID := range shiftRunIDs {
		idx, ok := indexByRunID[shiftRunID]
		if !ok {
			continue
		}
		end := idx + opts.NearShiftWindow
		if end >= len(nearMask) {
			end = len(nearMask) - 1
		}
		for i := idx; i <= end; i++ {
			nearMask[i] = true
		}
	}

	categoryTotals := make(map[string]int)
	offShiftRuns := 0
	for i, outcome := range outcomes {
		if !outcome.Analyzable {
			continue
		}
		score.AnalyzableRuns++
		score.TotalAlerts += outcome.AlertCount
		if nearMask[i] {
			score.NearShiftAlerts += outcome.AlertCount
		} else {
			offShiftRuns++
			score.OffShiftAlerts += outcome.AlertCount
		}
		for category, count := range outcome.CategoryMap {
			categoryTotals[category] += count
		}
	}

	if score.AnalyzableRuns > 0 {
		score.AlertsPerRun = float64(score.TotalAlerts) / float64(score.AnalyzableRuns)
	}
	if offShiftRuns > 0 {
		score.OffShiftAlertsPerRun = float64(score.OffShiftAlerts) / float64(offShiftRuns)
	}
	if score.TotalAlerts > 0 {
		score.NearShiftAlertFraction = float64(score.NearShiftAlerts) / float64(score.TotalAlerts)

		top := 0
		for _, count := range categoryTotals {
			if count > top {
				top = count
			}
			share := float64(count) / float64(score.TotalAlerts)
			score.CategoryHHI += share * share
		}
		score.CategoryTopShare = float64(top) / float64(score.TotalAlerts)
	}

	peakSum := 0.0
	persistenceSum := 0.0
	validShiftCount := 0
	for _, shiftRunID := range shiftRunIDs {
		idx, ok := indexByRunID[shiftRunID]
		if !ok {
			continue
		}
		end := idx + opts.PostShiftWindow
		if end >= len(outcomes) {
			end = len(outcomes) - 1
		}

		peak := 0
		for i := idx; i <= end; i++ {
			if outcomes[i].AlertCount > peak {
				peak = outcomes[i].AlertCount
			}
		}

		persistence := 0
		for i := idx; i <= end; i++ {
			if outcomes[i].AlertCount > 0 {
				persistence++
				continue
			}
			break
		}

		peakSum += float64(peak)
		persistenceSum += float64(persistence)
		validShiftCount++
	}
	if validShiftCount > 0 {
		score.ShiftEvents = validShiftCount
		score.BurstinessAfterShift = peakSum / float64(validShiftCount)
		score.PersistenceAfterShift = persistenceSum / float64(validShiftCount)
	}

	return score
}

func assignObjectiveScores(results []ConfigResult) {
	if len(results) == 0 {
		return
	}

	alertsRange := metricRangeFrom(results, func(r ConfigResult) float64 { return r.Scorecard.AlertsPerRun })
	burstRange := metricRangeFrom(results, func(r ConfigResult) float64 { return r.Scorecard.BurstinessAfterShift })
	persistRange := metricRangeFrom(results, func(r ConfigResult) float64 { return r.Scorecard.PersistenceAfterShift })
	hhiRange := metricRangeFrom(results, func(r ConfigResult) float64 { return r.Scorecard.CategoryHHI })
	nearRange := metricRangeFrom(results, func(r ConfigResult) float64 { return r.Scorecard.NearShiftAlertFraction })
	offShiftRange := metricRangeFrom(results, func(r ConfigResult) float64 { return r.Scorecard.OffShiftAlertsPerRun })

	for i := range results {
		penalties := []float64{
			normalizedPenalty(results[i].Scorecard.AlertsPerRun, alertsRange, false),
			normalizedPenalty(results[i].Scorecard.BurstinessAfterShift, burstRange, false),
			normalizedPenalty(results[i].Scorecard.PersistenceAfterShift, persistRange, false),
			normalizedPenalty(results[i].Scorecard.CategoryHHI, hhiRange, false),
			normalizedPenalty(results[i].Scorecard.NearShiftAlertFraction, nearRange, false),
			normalizedPenalty(results[i].Scorecard.OffShiftAlertsPerRun, offShiftRange, true),
		}
		sum := 0.0
		for _, p := range penalties {
			sum += p
		}
		results[i].ObjectiveScore = sum / float64(len(penalties))
	}
}

func applyRetentionConstraint(results []ConfigResult, minRetained float64) {
	if len(results) == 0 {
		return
	}

	maxOffShift := 0.0
	for _, result := range results {
		if result.Scorecard.OffShiftAlertsPerRun > maxOffShift {
			maxOffShift = result.Scorecard.OffShiftAlertsPerRun
		}
	}

	for i := range results {
		retained := 1.0
		if maxOffShift > 0 {
			retained = results[i].Scorecard.OffShiftAlertsPerRun / maxOffShift
		}
		results[i].Scorecard.RetainedOffShiftSignal = retained

		if minRetained <= 0 {
			results[i].EligibleForRecommendation = true
			continue
		}
		results[i].EligibleForRecommendation = retained+1e-9 >= minRetained
	}
}

func bestResultIndex(results []ConfigResult) int {
	if len(results) == 0 {
		return -1
	}

	bestEligible := -1
	for i := range results {
		if !results[i].EligibleForRecommendation {
			continue
		}
		if bestEligible == -1 || isBetterResult(results[i], results[bestEligible]) {
			bestEligible = i
		}
	}
	if bestEligible >= 0 {
		return bestEligible
	}

	best := 0
	for i := 1; i < len(results); i++ {
		if isBetterResult(results[i], results[best]) {
			best = i
		}
	}
	return best
}

func isBetterResult(a, b ConfigResult) bool {
	const eps = 1e-9
	if a.ObjectiveScore < b.ObjectiveScore-eps {
		return true
	}
	if a.ObjectiveScore > b.ObjectiveScore+eps {
		return false
	}
	if a.Scorecard.OffShiftAlertsPerRun > b.Scorecard.OffShiftAlertsPerRun+eps {
		return true
	}
	if a.Scorecard.OffShiftAlertsPerRun < b.Scorecard.OffShiftAlertsPerRun-eps {
		return false
	}
	if a.Scorecard.NearShiftAlertFraction < b.Scorecard.NearShiftAlertFraction-eps {
		return true
	}
	if a.Scorecard.NearShiftAlertFraction > b.Scorecard.NearShiftAlertFraction+eps {
		return false
	}
	if a.Scorecard.AlertsPerRun < b.Scorecard.AlertsPerRun-eps {
		return true
	}
	if a.Scorecard.AlertsPerRun > b.Scorecard.AlertsPerRun+eps {
		return false
	}
	return a.Config.Label() < b.Config.Label()
}

func computeShiftMetrics(getResults func(int64) ([]db.Result, error), newerRunID int64, olderRunID int64, opts Options) (shiftMetrics, error) {
	newerResults, err := getResults(newerRunID)
	if err != nil {
		return shiftMetrics{}, err
	}
	olderResults, err := getResults(olderRunID)
	if err != nil {
		return shiftMetrics{}, err
	}

	olderMap := make(map[db.BenchmarkKey]int64, len(olderResults))
	for _, result := range olderResults {
		olderMap[db.BenchmarkKey{Category: result.Category, Name: result.Name}] = result.AvgNs
	}

	compared := 0
	positive := 0
	logSum := 0.0
	for _, newer := range newerResults {
		older, ok := olderMap[db.BenchmarkKey{Category: newer.Category, Name: newer.Name}]
		if !ok || older <= 0 || newer.AvgNs <= 0 {
			continue
		}
		compared++
		if newer.AvgNs > older {
			positive++
		}
		logSum += math.Log(float64(newer.AvgNs) / float64(older))
	}
	if compared == 0 {
		return shiftMetrics{}, nil
	}

	share := float64(positive) / float64(compared)
	geoPct := (math.Exp(logSum/float64(compared)) - 1.0) * 100.0
	detected := compared >= opts.GlobalShiftMinBench &&
		share >= opts.GlobalShiftMinShare &&
		geoPct >= opts.GlobalShiftMinGeoPct

	return shiftMetrics{detected: detected, compared: compared, share: share, geoPct: geoPct}, nil
}

func resultToRunStat(runID int64, result db.Result) stats.RunStat {
	return stats.RunStat{
		RunID: runID,
		Avg:   float64(result.AvgNs),
	}
}

type metricRange struct {
	min float64
	max float64
}

func metricRangeFrom(results []ConfigResult, fn func(ConfigResult) float64) metricRange {
	r := metricRange{min: fn(results[0]), max: fn(results[0])}
	for i := 1; i < len(results); i++ {
		value := fn(results[i])
		if value < r.min {
			r.min = value
		}
		if value > r.max {
			r.max = value
		}
	}
	return r
}

func normalizedPenalty(value float64, r metricRange, higherIsBetter bool) float64 {
	if r.max <= r.min {
		return 0
	}
	if higherIsBetter {
		return (r.max - value) / (r.max - r.min)
	}
	return (value - r.min) / (r.max - r.min)
}

func uniqueSortedRunIDs(ids []int64) []int64 {
	if len(ids) == 0 {
		return nil
	}
	unique := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		unique[id] = struct{}{}
	}
	out := make([]int64, 0, len(unique))
	for id := range unique {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func filterKnownShiftRuns(shiftRunIDs []int64, replayRunIDs []int64) []int64 {
	if len(shiftRunIDs) == 0 || len(replayRunIDs) == 0 {
		return nil
	}
	known := make(map[int64]struct{}, len(replayRunIDs))
	for _, id := range replayRunIDs {
		known[id] = struct{}{}
	}
	filtered := make([]int64, 0, len(shiftRunIDs))
	for _, id := range shiftRunIDs {
		if _, ok := known[id]; ok {
			filtered = append(filtered, id)
		}
	}
	return uniqueSortedRunIDs(filtered)
}
