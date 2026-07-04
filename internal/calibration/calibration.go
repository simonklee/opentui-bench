// Package calibration provides the frozen Phase 6 chronological detector replay.
package calibration

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"

	"opentui-bench/internal/db"
	"opentui-bench/internal/stats"
)

const (
	ReportVersion    = "phase6-calibration-v1"
	CriteriaVersion  = "phase6-criteria-v1"
	AlgorithmVersion = "v7-phase6-calibration-rollout"
	DefaultSeed      = int64(1446)
	window           = 30
	minPoints        = 5
	baselineOffset   = 3
	fdr              = 0.01
	minAbsoluteNs    = 5000.0
	stableProxyPct   = 1.0
)

type Options struct {
	Branch string `json:"branch"`
	Seed   int64  `json:"seed"`
}

type FrozenConfig struct {
	Version                 string  `json:"version"`
	AlgorithmVersion        string  `json:"algorithm_version"`
	Metric                  string  `json:"metric"`
	Estimator               string  `json:"estimator"`
	CohortPolicy            string  `json:"cohort_policy"`
	Ordering                string  `json:"ordering"`
	Window                  int     `json:"window"`
	MinPoints               int     `json:"min_points"`
	BaselineOffset          int     `json:"baseline_offset"`
	FDR                     float64 `json:"score_family_threshold"`
	MinRelativePercent      float64 `json:"min_relative_percent"`
	MinAbsoluteNs           float64 `json:"min_absolute_ns"`
	SparseInjectionFraction float64 `json:"sparse_injection_fraction"`
}

type DetectorSummary struct {
	Label               string  `json:"label"`
	Production          bool    `json:"production"`
	Snapshots           int     `json:"snapshots"`
	EligibleSnapshots   int     `json:"eligible_snapshots"`
	CandidateHypotheses int     `json:"candidate_hypotheses"`
	EligibleHypotheses  int     `json:"eligible_hypotheses"`
	Coverage            float64 `json:"coverage"`
	Alerts              int     `json:"alerts"`
	AlertHypothesisRate float64 `json:"alert_hypothesis_rate"`
	AlertSnapshotRate   float64 `json:"alert_snapshot_rate"`
}

type PBin struct {
	Lower              float64 `json:"lower"`
	Upper              float64 `json:"upper"`
	NominalProbability float64 `json:"nominal_probability"`
	Count              int     `json:"count"`
	EmpiricalRate      float64 `json:"empirical_rate"`
}

type PQuantile struct {
	Probability float64 `json:"probability"`
	Nominal     float64 `json:"nominal"`
	Empirical   float64 `json:"empirical"`
}

type NullEvidence struct {
	Label              string      `json:"label"`
	Status             string      `json:"status"`
	Detail             string      `json:"detail"`
	Groups             int         `json:"groups"`
	Snapshots          int         `json:"snapshots"`
	EligibleHypotheses int         `json:"eligible_hypotheses"`
	Alerts             int         `json:"alerts"`
	FalseAlertRate     *float64    `json:"false_alert_rate,omitempty"`
	PBins              []PBin      `json:"p_value_bins"`
	PQuantiles         []PQuantile `json:"p_value_quantiles"`
}

type AutocorrelationReport struct {
	Status                    string  `json:"status"`
	Histories                 int     `json:"histories"`
	HistoriesWithTenResiduals int     `json:"histories_with_at_least_10_residuals"`
	Coverage                  float64 `json:"coverage"`
	MaterialThreshold         float64 `json:"material_absolute_lag1_threshold"`
	MaterialHistories         int     `json:"material_histories"`
	MaterialShare             float64 `json:"material_share"`
	MeanLag1                  float64 `json:"mean_lag1"`
	MaterialDependence        bool    `json:"material_dependence"`
}

type InjectionResult struct {
	Label          string   `json:"label"`
	EffectPercent  float64  `json:"effect_percent"`
	Broad          bool     `json:"broad"`
	Hypotheses     int      `json:"hypotheses"`
	Injected       int      `json:"injected"`
	Detected       int      `json:"detected"`
	DetectionRate  *float64 `json:"detection_rate"`
	Unchanged      int      `json:"unchanged"`
	FalseAlerts    int      `json:"false_alerts"`
	FalseAlertRate *float64 `json:"false_alert_rate"`
}

type MetadataField struct {
	Field    string  `json:"field"`
	Status   string  `json:"status"`
	Coverage float64 `json:"coverage"`
	Detail   string  `json:"detail"`
}

type Transition struct {
	Field     string `json:"field"`
	RunID     int64  `json:"run_id"`
	RunDate   string `json:"run_date"`
	From      string `json:"from"`
	To        string `json:"to"`
	Evaluated bool   `json:"evaluated"`
	Detail    string `json:"detail"`
}

type MetadataReport struct {
	Fields      []MetadataField `json:"fields"`
	Transitions []Transition    `json:"transitions"`
}

type Criterion struct {
	Name      string `json:"name"`
	Required  string `json:"required"`
	Observed  string `json:"observed"`
	Status    string `json:"status"`
	Rationale string `json:"rationale,omitempty"`
}

type Decision struct {
	Status           string      `json:"status"`
	FormalGuarantees bool        `json:"formal_p_value_or_fdr_guarantees"`
	Summary          string      `json:"summary"`
	Criteria         []Criterion `json:"criteria"`
}

type Report struct {
	ReportVersion      string                `json:"report_version"`
	CriteriaVersion    string                `json:"criteria_version"`
	CriteriaProvenance string                `json:"criteria_provenance"`
	InputFingerprint   string                `json:"input_sha256"`
	AsOf               string                `json:"as_of"`
	Options            Options               `json:"options"`
	Config             FrozenConfig          `json:"frozen_config"`
	NewDetector        DetectorSummary       `json:"new_detector"`
	Legacy             DetectorSummary       `json:"legacy_diagnostic_only"`
	RepeatedNull       NullEvidence          `json:"repeated_unchanged_commit_null"`
	StableProxy        NullEvidence          `json:"stable_period_proxy"`
	Autocorrelation    AutocorrelationReport `json:"lag1_residual_autocorrelation"`
	Injections         []InjectionResult     `json:"synthetic_injections"`
	Metadata           MetadataReport        `json:"metadata_transitions"`
	Decision           Decision              `json:"decision"`
}

type runData struct {
	Run     db.Run
	At      time.Time
	Results []db.Result
}

type historyKey struct {
	Machine, Optimize, Category, Name string
}

type point struct {
	Run    db.Run
	At     time.Time
	Result db.Result
}

type hypothesis struct {
	Key         historyKey
	Result      db.Result
	Evaluation  stats.SnapshotEvaluation
	LegacyP     *float64
	LegacyOK    bool
	Alert       bool
	LegacyAlert bool
}

type snapshot struct {
	Run           db.Run
	Candidates    int
	Hypotheses    []hypothesis
	RepeatedNull  bool
	RepeatedGroup string
	StableProxy   bool
}

func DefaultOptions() Options { return Options{Branch: "main", Seed: DefaultSeed} }

func frozenConfig() FrozenConfig {
	return FrozenConfig{
		Version: ReportVersion, AlgorithmVersion: AlgorithmVersion, Metric: "log(avg_ns)",
		Estimator: "historical_log_mean_prediction", CohortPolicy: "exact_(category,name,machine_id,zig_optimize)_as_of",
		Ordering: "strict_(run_date,id)", Window: window, MinPoints: minPoints,
		BaselineOffset: baselineOffset, FDR: fdr,
		MinRelativePercent: stats.MinPracticalRegressionEffectPercent, MinAbsoluteNs: minAbsoluteNs,
		SparseInjectionFraction: 0.20,
	}
}

// Run loads and replays the database without writing to it.
func Run(database *db.DB, opts Options) (*Report, error) {
	if strings.TrimSpace(opts.Branch) == "" {
		opts.Branch = "main"
	}
	runs, err := loadRuns(database)
	if err != nil {
		return nil, err
	}
	snapshots, selected, err := replay(runs, opts.Branch)
	if err != nil {
		return nil, err
	}
	report := buildReport(snapshots, selected, opts, inputFingerprint(runs))
	return &report, nil
}

func loadRuns(database *db.DB) ([]runData, error) {
	rows, err := database.Query(`SELECT id FROM runs ORDER BY julianday(run_date), id`)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	runs := make([]runData, 0, len(ids))
	for _, id := range ids {
		run, err := database.GetRun(id)
		if err != nil {
			return nil, err
		}
		at, err := time.Parse(time.RFC3339Nano, run.RunDate)
		if err != nil {
			return nil, fmt.Errorf("parse run %d date: %w", id, err)
		}
		results, err := loadCalibrationResults(database, id)
		if err != nil {
			return nil, err
		}
		runs = append(runs, runData{Run: *run, At: at, Results: results})
	}
	return runs, nil
}

// Calibration intentionally reads only columns that existed before the sample
// precision migration. This permits read-only replay of an unmigrated
// production copy without weakening the detector's feature semantics.
func loadCalibrationResults(database *db.DB, runID int64) ([]db.Result, error) {
	rows, err := database.Query(`
		SELECT id, run_id, category, name, min_ns, avg_ns, max_ns,
		       COALESCE(std_dev_ns, 0), COALESCE(p50_ns, 0), COALESCE(p95_ns, 0),
		       COALESCE(p99_ns, 0), total_ns, iterations, COALESCE(sample_count, 1)
		FROM results WHERE run_id = ? ORDER BY category, name`, runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var results []db.Result
	for rows.Next() {
		var result db.Result
		if err := rows.Scan(
			&result.ID, &result.RunID, &result.Category, &result.Name,
			&result.MinNs, &result.AvgNs, &result.MaxNs, &result.StdDevNs,
			&result.P50Ns, &result.P95Ns, &result.P99Ns, &result.TotalNs,
			&result.Iterations, &result.SampleCount,
		); err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, rows.Err()
}

func normalizedBranch(branch string) string {
	if branch == "" {
		return "main"
	}
	return branch
}

func replay(runs []runData, branch string) ([]snapshot, []runData, error) {
	sort.SliceStable(runs, func(i, j int) bool {
		if runs[i].At.Equal(runs[j].At) {
			return runs[i].Run.ID < runs[j].Run.ID
		}
		return runs[i].At.Before(runs[j].At)
	})
	history := make(map[historyKey][]point)
	commitSeen := make(map[string]int)
	var previousByCohort = make(map[string]runData)
	var snapshots []snapshot
	var selected []runData
	for _, current := range runs {
		isTarget := normalizedBranch(current.Run.Branch) == branch
		cohort := current.Run.MachineID + "\x00" + current.Run.ZigOptimize
		if isTarget {
			s := snapshot{Run: current.Run, Candidates: len(current.Results)}
			commitGroup := cohort + "\x00" + current.Run.CommitHashFull
			if current.Run.CommitHashFull != "" && commitSeen[commitGroup] > 0 {
				s.RepeatedNull = true
				s.RepeatedGroup = commitGroup
			}
			if previous, ok := previousByCohort[cohort]; ok {
				s.StableProxy = stableTransition(previous, current)
			}
			for _, result := range current.Results {
				if current.Run.MachineID == "" || current.Run.ZigOptimize == "" || result.AvgNs <= 0 {
					continue
				}
				key := historyKey{current.Run.MachineID, current.Run.ZigOptimize, result.Category, result.Name}
				prior := history[key]
				observations := make([]stats.OrderedRunStat, 0, len(prior)+1)
				for _, p := range prior {
					observations = append(observations, stats.OrderedRunStat{RunDate: p.At, Stat: stats.RunStat{RunID: p.Run.ID, Avg: float64(p.Result.AvgNs)}})
				}
				target := stats.OrderedRunStat{RunDate: current.At, Stat: stats.RunStat{RunID: current.Run.ID, Avg: float64(result.AvgNs)}}
				observations = append(observations, target)
				evaluation := stats.EvaluateSnapshot(target, observations, stats.SnapshotConfig{Window: window, MinPoints: minPoints, BaselineOffset: baselineOffset})
				if evaluation.Baseline == nil {
					continue
				}
				legacyP, legacyOK := legacyScore(result, prior)
				s.Hypotheses = append(s.Hypotheses, hypothesis{Key: key, Result: result, Evaluation: evaluation, LegacyP: legacyP, LegacyOK: legacyOK})
			}
			applyAlerts(&s)
			snapshots = append(snapshots, s)
			selected = append(selected, current)
			previousByCohort[cohort] = current
			if current.Run.CommitHashFull != "" {
				commitSeen[cohort+"\x00"+current.Run.CommitHashFull]++
			}
		}
		// Production feature targets use main history; feature observations never
		// become baseline points. Main/legacy-main observations do.
		if normalizedBranch(current.Run.Branch) == "main" && current.Run.MachineID != "" && current.Run.ZigOptimize != "" {
			for _, result := range current.Results {
				key := historyKey{current.Run.MachineID, current.Run.ZigOptimize, result.Category, result.Name}
				history[key] = append(history[key], point{Run: current.Run, At: current.At, Result: result})
			}
		}
	}
	return snapshots, selected, nil
}

func applyAlerts(s *snapshot) {
	p := make([]float64, len(s.Hypotheses))
	for i := range s.Hypotheses {
		p[i] = *s.Hypotheses[i].Evaluation.Result.PValue
	}
	for _, adjusted := range stats.BenjaminiHochberg(p, fdr) {
		h := &s.Hypotheses[adjusted.Index]
		h.Alert = adjusted.IsSignificant && *h.Evaluation.Result.ChangePercent >= stats.MinPracticalRegressionEffectPercent && *h.Evaluation.Result.AbsoluteChangeNs >= minAbsoluteNs
	}
	var legacyIndexes []int
	var legacyP []float64
	for i := range s.Hypotheses {
		if s.Hypotheses[i].LegacyP != nil {
			legacyIndexes = append(legacyIndexes, i)
			legacyP = append(legacyP, *s.Hypotheses[i].LegacyP)
		}
	}
	for _, adjusted := range stats.BenjaminiHochberg(legacyP, fdr) {
		h := &s.Hypotheses[legacyIndexes[adjusted.Index]]
		h.LegacyAlert = adjusted.IsSignificant && h.LegacyOK
	}
}

func legacyScore(target db.Result, prior []point) (*float64, bool) {
	if target.SampleCount < 2 || target.StdDevNs <= 0 {
		return nil, false
	}
	if baselineOffset >= len(prior) {
		return nil, false
	}
	end := len(prior) - baselineOffset
	start := end - window
	if start < 0 {
		start = 0
	}
	selected := prior[start:end]
	var medians, sem2 []float64
	for _, p := range selected {
		if p.Result.SampleCount < 2 || p.Result.StdDevNs <= 0 {
			continue
		}
		medians = append(medians, float64(p.Result.P50Ns))
		sem := float64(p.Result.StdDevNs) / math.Sqrt(float64(p.Result.SampleCount))
		sem2 = append(sem2, sem*sem)
	}
	if len(medians) < minPoints {
		return nil, false
	}
	baseline := median(medians)
	meanMedian := mean(medians)
	runVariance := variance(medians, meanMedian)
	combined := runVariance / float64(len(medians))
	if len(medians) >= 10 {
		combined = (mean(sem2) + math.Max(0, runVariance-mean(sem2))) / float64(len(medians))
	}
	latestSEM := float64(target.StdDevNs) / math.Sqrt(float64(target.SampleCount))
	se := math.Sqrt(latestSEM*latestSEM + combined)
	if se == 0 {
		return nil, false
	}
	score := (float64(target.P50Ns) - baseline) / se
	p := stats.OneSidedTPValue(score, len(medians)-1)
	cv := 0.0
	if meanMedian > 0 {
		cv = math.Sqrt(runVariance) / meanMedian
	}
	minEffect := math.Max(stats.MinPracticalRegressionEffectPercent, 2*cv*100)
	change := 0.0
	if baseline > 0 {
		change = (float64(target.P50Ns) - baseline) / baseline * 100
	}
	return &p, change >= minEffect && float64(target.P50Ns)-baseline >= minAbsoluteNs
}

func stableTransition(previous, current runData) bool {
	prev := make(map[db.BenchmarkKey]int64, len(previous.Results))
	for _, r := range previous.Results {
		if r.AvgNs > 0 {
			prev[db.BenchmarkKey{Category: r.Category, Name: r.Name}] = r.AvgNs
		}
	}
	var logs []float64
	for _, r := range current.Results {
		if old := prev[db.BenchmarkKey{Category: r.Category, Name: r.Name}]; old > 0 && r.AvgNs > 0 {
			logs = append(logs, math.Log(float64(r.AvgNs)/float64(old)))
		}
	}
	return len(logs) >= 10 && math.Abs((math.Exp(median(logs))-1)*100) <= stableProxyPct
}

func buildReport(snapshots []snapshot, runs []runData, opts Options, inputHash string) Report {
	report := Report{
		ReportVersion: ReportVersion, CriteriaVersion: CriteriaVersion,
		CriteriaProvenance: "Versioned with this implementation; no prior committed preregistration exists.",
		InputFingerprint:   inputHash, Options: opts, Config: frozenConfig(),
	}
	if len(runs) > 0 {
		report.AsOf = runs[len(runs)-1].Run.RunDate
	}
	report.NewDetector = summarize(snapshots, false)
	report.Legacy = summarize(snapshots, true)
	report.RepeatedNull = nullReport("Repeated unchanged-commit groups with exact machine/optimize cohort", snapshots, func(s snapshot) bool { return s.RepeatedNull })
	report.StableProxy = nullReport("Stable-period proxy: consecutive compatible runs with median common-benchmark movement within 1%; commits may differ", snapshots, func(s snapshot) bool { return s.StableProxy })
	report.Autocorrelation = autocorrelation(snapshots)
	report.Injections = injections(snapshots, opts.Seed)
	report.Metadata = metadataReport(runs, snapshots)
	report.Decision = decide(report)
	return report
}

func summarize(snapshots []snapshot, legacy bool) DetectorSummary {
	label := "Phase 3 log-average prediction score"
	if legacy {
		label = "LEGACY DIAGNOSTIC ONLY: median/SEM detector"
	}
	s := DetectorSummary{Label: label, Production: !legacy, Snapshots: len(snapshots)}
	alertSnapshots := 0
	for _, snap := range snapshots {
		s.CandidateHypotheses += snap.Candidates
		eligible, alerts := 0, 0
		for _, h := range snap.Hypotheses {
			if legacy && h.LegacyP == nil {
				continue
			}
			eligible++
			if (!legacy && h.Alert) || (legacy && h.LegacyAlert) {
				alerts++
			}
		}
		if eligible > 0 {
			s.EligibleSnapshots++
			s.EligibleHypotheses += eligible
		}
		if alerts > 0 {
			alertSnapshots++
		}
		s.Alerts += alerts
	}
	if s.CandidateHypotheses > 0 {
		s.Coverage = float64(s.EligibleHypotheses) / float64(s.CandidateHypotheses)
	}
	if s.EligibleHypotheses > 0 {
		s.AlertHypothesisRate = float64(s.Alerts) / float64(s.EligibleHypotheses)
	}
	if s.EligibleSnapshots > 0 {
		s.AlertSnapshotRate = float64(alertSnapshots) / float64(s.EligibleSnapshots)
	}
	return s
}

func nullReport(label string, snapshots []snapshot, include func(snapshot) bool) NullEvidence {
	n := NullEvidence{Label: label, Status: "unavailable", PBins: []PBin{}, PQuantiles: []PQuantile{}}
	var values []float64
	groups := make(map[string]struct{})
	for _, s := range snapshots {
		if include(s) {
			n.Snapshots++
			group := s.RepeatedGroup
			if group == "" {
				group = fmt.Sprintf("run:%d", s.Run.ID)
			}
			groups[group] = struct{}{}
			for _, h := range s.Hypotheses {
				values = append(values, *h.Evaluation.Result.PValue)
				n.EligibleHypotheses++
				if h.Alert {
					n.Alerts++
				}
			}
		}
	}
	n.Groups = len(groups)
	if len(values) == 0 {
		n.Detail = "No eligible observations support this null proxy; no evidence was invented."
		return n
	}
	n.Status = "available"
	rate := float64(n.Alerts) / float64(len(values))
	n.FalseAlertRate = &rate
	n.PBins, n.PQuantiles = pCalibration(values)
	n.Detail = "Empirical behavior is descriptive for this labeled proxy, not proof of a true null."
	return n
}

func pCalibration(values []float64) ([]PBin, []PQuantile) {
	edges := []float64{0, .01, .05, .10, .25, .50, 1.0000000001}
	bins := make([]PBin, len(edges)-1)
	for i := range bins {
		bins[i] = PBin{Lower: edges[i], Upper: math.Min(1, edges[i+1]), NominalProbability: math.Min(1, edges[i+1]) - edges[i]}
	}
	for _, p := range values {
		for i := range bins {
			if p >= edges[i] && p < edges[i+1] {
				bins[i].Count++
				break
			}
		}
	}
	for i := range bins {
		if len(values) > 0 {
			bins[i].EmpiricalRate = float64(bins[i].Count) / float64(len(values))
		}
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	probs := []float64{.01, .05, .10, .25, .50, .75, .90, .95, .99}
	quantiles := make([]PQuantile, len(probs))
	for i, q := range probs {
		quantiles[i] = PQuantile{Probability: q, Nominal: q, Empirical: quantile(sorted, q)}
	}
	return bins, quantiles
}

func autocorrelation(snapshots []snapshot) AutocorrelationReport {
	r := AutocorrelationReport{Status: "unavailable", MaterialThreshold: .30}
	series := make(map[historyKey][]float64)
	for _, s := range snapshots {
		for _, h := range s.Hypotheses {
			residual := math.Log(float64(h.Result.AvgNs)) - h.Evaluation.Baseline.LogMean
			series[h.Key] = append(series[h.Key], residual)
		}
	}
	r.Histories = len(series)
	var correlations []float64
	for _, values := range series {
		if len(values) < 10 {
			continue
		}
		r.HistoriesWithTenResiduals++
		correlations = append(correlations, lag1(values))
	}
	if r.Histories > 0 {
		r.Coverage = float64(r.HistoriesWithTenResiduals) / float64(r.Histories)
	}
	if len(correlations) == 0 {
		return r
	}
	sort.Float64s(correlations)
	r.Status = "available"
	for _, value := range correlations {
		r.MeanLag1 += value
		if math.Abs(value) >= r.MaterialThreshold {
			r.MaterialHistories++
		}
	}
	r.MeanLag1 /= float64(len(correlations))
	r.MaterialShare = float64(r.MaterialHistories) / float64(len(correlations))
	r.MaterialDependence = r.MaterialShare > 0.20
	return r
}

func lag1(values []float64) float64 {
	if len(values) < 3 {
		return 0
	}
	x, y := values[:len(values)-1], values[1:]
	mx, my := mean(x), mean(y)
	var numerator, dx, dy float64
	for i := range x {
		a, b := x[i]-mx, y[i]-my
		numerator += a * b
		dx += a * a
		dy += b * b
	}
	if dx == 0 || dy == 0 {
		return 0
	}
	return numerator / math.Sqrt(dx*dy)
}

func injections(snapshots []snapshot, seed int64) []InjectionResult {
	specs := []struct {
		label  string
		effect float64
		broad  bool
	}{{"unchanged_synthetic_null", 0, false}, {"sparse_2_percent", 2, false}, {"sparse_5_percent", 5, false}, {"sparse_10_percent", 10, false}, {"broad_2_percent", 2, true}, {"broad_5_percent", 5, true}, {"broad_10_percent", 10, true}}
	out := make([]InjectionResult, 0, len(specs))
	for _, spec := range specs {
		r := InjectionResult{Label: spec.label, EffectPercent: spec.effect, Broad: spec.broad}
		for _, s := range snapshots {
			p := make([]float64, len(s.Hypotheses))
			injected := make([]bool, len(s.Hypotheses))
			practical := make([]bool, len(s.Hypotheses))
			for i, h := range s.Hypotheses {
				residuals := make([]float64, len(h.Evaluation.History))
				for j, history := range h.Evaluation.History {
					residuals[j] = math.Log(history.Avg) - h.Evaluation.Baseline.LogMean
				}
				hash := stableHash(seed, s.Run.ID, h.Key)
				residual := residuals[int(hash%uint64(len(residuals)))]
				isInjected := spec.effect > 0 && (spec.broad || hash%5 == 0)
				injected[i] = isInjected
				effect := 0.0
				if isInjected {
					effect = spec.effect
				}
				avg := math.Exp(h.Evaluation.Baseline.LogMean+residual) * (1 + effect/100)
				result := stats.DetectRegression(stats.RunStat{RunID: s.Run.ID, Avg: avg}, h.Evaluation.Baseline)
				p[i] = *result.PValue
				practical[i] = *result.ChangePercent >= stats.MinPracticalRegressionEffectPercent && *result.AbsoluteChangeNs >= minAbsoluteNs
			}
			for _, adjusted := range stats.BenjaminiHochberg(p, fdr) {
				alert := adjusted.IsSignificant && practical[adjusted.Index]
				r.Hypotheses++
				if injected[adjusted.Index] {
					r.Injected++
					if alert {
						r.Detected++
					}
				} else {
					r.Unchanged++
					if alert {
						r.FalseAlerts++
					}
				}
			}
		}
		if r.Injected > 0 {
			rate := float64(r.Detected) / float64(r.Injected)
			r.DetectionRate = &rate
		}
		if r.Unchanged > 0 {
			rate := float64(r.FalseAlerts) / float64(r.Unchanged)
			r.FalseAlertRate = &rate
		}
		out = append(out, r)
	}
	return out
}

func stableHash(seed int64, runID int64, key historyKey) uint64 {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%d\x00%s\x00%s\x00%s\x00%s", seed, runID, key.Machine, key.Optimize, key.Category, key.Name)))
	return binary.LittleEndian.Uint64(sum[:8])
}

func inputFingerprint(runs []runData) string {
	hash := sha256.New()
	for _, run := range runs {
		_, _ = fmt.Fprintf(hash, "%d:%q:%q:%q:%q:%q:%q:%q\n",
			run.Run.ID, run.Run.CommitHash, run.Run.CommitHashFull, run.Run.Branch,
			run.Run.RunDate, run.Run.MachineID, run.Run.ZigOptimize, run.Run.CommitMessage)
		for _, result := range run.Results {
			_, _ = fmt.Fprintf(hash, "%d:%q:%q:%d:%d:%d:%d\n",
				result.ID, result.Category, result.Name, result.AvgNs,
				result.P50Ns, result.StdDevNs, result.SampleCount)
		}
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func metadataReport(runs []runData, snapshots []snapshot) MetadataReport {
	m := MetadataReport{Transitions: []Transition{}}
	snapshotByRun := make(map[int64]snapshot, len(snapshots))
	for _, snapshot := range snapshots {
		snapshotByRun[snapshot.Run.ID] = snapshot
	}
	fields := []struct {
		name        string
		value       func(db.Run) string
		unavailable string
	}{{"machine_id", func(r db.Run) string { return r.MachineID }, ""}, {"zig_optimize", func(r db.Run) string { return r.ZigOptimize }, ""}, {"zig_toolchain_version", nil, "not stored"}, {"harness_version", nil, "not stored"}}
	for _, field := range fields {
		if field.value == nil {
			m.Fields = append(m.Fields, MetadataField{Field: field.name, Status: "unavailable", Detail: field.unavailable})
			continue
		}
		known := 0
		for _, run := range runs {
			if field.value(run.Run) != "" {
				known++
			}
		}
		coverage := 0.0
		if len(runs) > 0 {
			coverage = float64(known) / float64(len(runs))
		}
		status := "available"
		if known == 0 {
			status = "unavailable"
		}
		m.Fields = append(m.Fields, MetadataField{Field: field.name, Status: status, Coverage: coverage, Detail: "Transitions are consecutive selected-branch metadata changes; absence is not evidence that no external change occurred."})
		for i := 1; i < len(runs); i++ {
			from, to := field.value(runs[i-1].Run), field.value(runs[i].Run)
			if from != "" && to != "" && from != to {
				transition := Transition{Field: field.name, RunID: runs[i].Run.ID, RunDate: runs[i].Run.RunDate, From: from, To: to}
				if snapshot, ok := snapshotByRun[runs[i].Run.ID]; ok && len(snapshot.Hypotheses) > 0 {
					alerts := 0
					for _, hypothesis := range snapshot.Hypotheses {
						if hypothesis.Alert {
							alerts++
						}
					}
					transition.Evaluated = true
					transition.Detail = fmt.Sprintf("Target snapshot had %d eligible hypotheses and %d alerts; this is descriptive, not causal attribution.", len(snapshot.Hypotheses), alerts)
				} else {
					transition.Detail = "Insufficient compatible prior history at the transition; behavior is unavailable."
				}
				m.Transitions = append(m.Transitions, transition)
			}
		}
	}
	return m
}

func decide(report Report) Decision {
	criteria := []Criterion{}
	add := func(name, required, observed, status, rationale string) {
		criteria = append(criteria, Criterion{Name: name, Required: required, Observed: observed, Status: status, Rationale: rationale})
	}
	coverageStatus := "fail"
	if report.NewDetector.EligibleSnapshots >= 50 && report.NewDetector.Coverage >= .50 {
		coverageStatus = "pass"
	}
	add("chronological_coverage", ">=50 eligible snapshots and >=50% hypothesis coverage", fmt.Sprintf("%d snapshots, %.1f%% coverage", report.NewDetector.EligibleSnapshots, report.NewDetector.Coverage*100), coverageStatus, "")
	repeatedStatus := "unavailable"
	if report.RepeatedNull.Groups >= 5 && report.RepeatedNull.Snapshots >= 20 && report.RepeatedNull.EligibleHypotheses >= 1000 {
		repeatedStatus = "fail"
		if report.RepeatedNull.FalseAlertRate != nil && *report.RepeatedNull.FalseAlertRate <= .01 {
			repeatedStatus = "pass"
		}
	}
	add("repeated_commit_null", ">=5 groups, >=20 snapshots, >=1000 eligible hypotheses, <=1% false-alert hypotheses", fmt.Sprintf("%d groups, %d snapshots, %d hypotheses", report.RepeatedNull.Groups, report.RepeatedNull.Snapshots, report.RepeatedNull.EligibleHypotheses), repeatedStatus, "True unchanged-commit evidence is mandatory.")
	stableStatus := "unavailable"
	if report.StableProxy.Snapshots >= 20 && report.StableProxy.EligibleHypotheses >= 1000 {
		stableStatus = "fail"
		if report.StableProxy.FalseAlertRate != nil && *report.StableProxy.FalseAlertRate <= .01 {
			stableStatus = "pass"
		}
	}
	add("stable_period_proxy", ">=20 snapshots, >=1000 eligible hypotheses, <=1% false-alert hypotheses", fmt.Sprintf("%d snapshots, %d hypotheses", report.StableProxy.Snapshots, report.StableProxy.EligibleHypotheses), stableStatus, "Proxy is supplementary and cannot replace repeated commits.")
	autoStatus := "unavailable"
	if report.Autocorrelation.HistoriesWithTenResiduals >= 20 && report.Autocorrelation.Coverage >= .50 {
		autoStatus = "fail"
		if !report.Autocorrelation.MaterialDependence {
			autoStatus = "pass"
		}
	}
	add("residual_dependence", ">=20 histories, >=50% coverage, <=20% with |lag-1| >=0.30", fmt.Sprintf("%d histories, %.1f%% coverage, %.1f%% material", report.Autocorrelation.HistoriesWithTenResiduals, report.Autocorrelation.Coverage*100, report.Autocorrelation.MaterialShare*100), autoStatus, "")
	injectionStatus := "pass"
	requirements := map[string]float64{"unchanged_synthetic_null": .01, "sparse_2_percent": .50, "sparse_5_percent": .80, "sparse_10_percent": .95, "broad_2_percent": .50, "broad_5_percent": .80, "broad_10_percent": .95}
	var observed []string
	for _, r := range report.Injections {
		observed = append(observed, fmt.Sprintf("%s det=%s false=%s", r.Label, formatOptionalRate(r.DetectionRate), formatOptionalRate(r.FalseAlertRate)))
		if r.Label == "unchanged_synthetic_null" {
			if r.FalseAlertRate == nil || *r.FalseAlertRate > requirements[r.Label] {
				injectionStatus = "fail"
			}
		} else if r.Injected == 0 || r.DetectionRate == nil || *r.DetectionRate < requirements[r.Label] {
			injectionStatus = "fail"
		}
	}
	add("synthetic_injections", "null false alerts <=1%; detection >=50%/80%/95% at sparse and broad 2%/5%/10%", strings.Join(observed, "; "), injectionStatus, "Parameters and baselines were frozen before deterministic residual-bootstrap injections.")
	transitionStatus := "unavailable"
	if len(report.Metadata.Transitions) > 0 {
		transitionStatus = "fail"
	}
	add("environment_transitions", "stored machine, toolchain, and harness transitions evaluated with before/after coverage", fmt.Sprintf("%d stored transitions; toolchain and harness versions unavailable", len(report.Metadata.Transitions)), transitionStatus, "No pass is possible when required transition metadata/evidence is absent.")
	status := "calibrated"
	for _, c := range criteria {
		if c.Status != "pass" {
			status = "uncalibrated_regression_score"
			break
		}
	}
	summary := "All versioned acceptance criteria passed with adequate evidence."
	formal := status == "calibrated"
	if !formal {
		summary = "Retain uncalibrated regression-score wording. At least one versioned criterion failed or lacked adequate evidence; no formal p-value or FDR guarantee is claimed."
	}
	return Decision{Status: status, FormalGuarantees: formal, Summary: summary, Criteria: criteria}
}

func formatOptionalRate(rate *float64) string {
	if rate == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.1f%%", *rate*100)
}

func mean(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}
func variance(v []float64, m float64) float64 {
	if len(v) < 2 {
		return 0
	}
	s := 0.0
	for _, x := range v {
		d := x - m
		s += d * d
	}
	return s / float64(len(v)-1)
}
func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	x := append([]float64(nil), v...)
	sort.Float64s(x)
	if len(x)%2 == 1 {
		return x[len(x)/2]
	}
	return (x[len(x)/2-1] + x[len(x)/2]) / 2
}
func quantile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	pos := q * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if lo == hi {
		return sorted[lo]
	}
	return sorted[lo] + (sorted[hi]-sorted[lo])*(pos-float64(lo))
}

func WriteJSON(w io.Writer, report *Report) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

func WriteText(w io.Writer, report *Report) {
	fmt.Fprintf(w, "Phase 6 Calibration Report (%s)\n", report.ReportVersion)
	fmt.Fprintf(w, "As of: %s | Branch: %s | Seed: %d\n", report.AsOf, report.Options.Branch, report.Options.Seed)
	fmt.Fprintf(w, "Input SHA-256: %s | Criteria: %s\n", report.InputFingerprint, report.CriteriaVersion)
	fmt.Fprintf(w, "Decision: %s\n%s\n\n", report.Decision.Status, report.Decision.Summary)
	fmt.Fprintf(w, "Detector comparison\n  New:    %d/%d eligible (%.1f%%), %d alerts, %.2f%% alert hypotheses\n", report.NewDetector.EligibleHypotheses, report.NewDetector.CandidateHypotheses, report.NewDetector.Coverage*100, report.NewDetector.Alerts, report.NewDetector.AlertHypothesisRate*100)
	fmt.Fprintf(w, "  Legacy diagnostic only: %d/%d eligible (%.1f%%), %d alerts, %.2f%% alert hypotheses\n\n", report.Legacy.EligibleHypotheses, report.Legacy.CandidateHypotheses, report.Legacy.Coverage*100, report.Legacy.Alerts, report.Legacy.AlertHypothesisRate*100)
	fmt.Fprintf(w, "Null evidence\n  Repeated commits: %s (%d snapshots, %d hypotheses)\n  Stable-period proxy: %s (%d snapshots, %d hypotheses)\n", report.RepeatedNull.Status, report.RepeatedNull.Snapshots, report.RepeatedNull.EligibleHypotheses, report.StableProxy.Status, report.StableProxy.Snapshots, report.StableProxy.EligibleHypotheses)
	fmt.Fprintf(w, "  Lag-1 residuals: %s, %.1f%% material among %d sufficiently long histories\n\n", report.Autocorrelation.Status, report.Autocorrelation.MaterialShare*100, report.Autocorrelation.HistoriesWithTenResiduals)
	fmt.Fprintf(w, "Versioned acceptance criteria\n")
	for _, c := range report.Decision.Criteria {
		fmt.Fprintf(w, "  %-24s %-11s %s\n", c.Name, c.Status, c.Observed)
	}
}
