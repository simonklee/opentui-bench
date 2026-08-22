package runner

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"opentui-bench/internal/db"
	"opentui-bench/internal/jsbench"
	"opentui-bench/internal/record"
)

type ProfileMode string

type BenchmarkKind string
type JavaScriptRuntime string

const (
	ProfileNone  ProfileMode       = "none"
	ProfileCPU   ProfileMode       = "cpu"
	BenchmarkZig BenchmarkKind     = "zig"
	BenchmarkJS  BenchmarkKind     = jsbench.Kind
	RuntimeBun   JavaScriptRuntime = jsbench.RuntimeBun
	RuntimeNode  JavaScriptRuntime = jsbench.RuntimeNode
)

type RunConfig struct {
	RepoPath        string
	ZigOptimize     string
	Filter          string
	FilterBenchmark string
	Benchmarks      []string
	Samples         int
	Profile         ProfileMode
	PerfFreq        int
	Notes           string
	MachineID       string
	WorkDir         string
	Branch          string // override branch name (useful for detached HEAD)
	BenchmarkKind   BenchmarkKind
	BenchmarkSuite  string
	ProtocolVersion int64
	BunVersion      string
	JSRuntime       JavaScriptRuntime
	RuntimeVersion  string
	ZigVersion      string
	ManifestHash    string
}

// CollectedArtifact holds a binary artifact (e.g., pprof profile) captured
// during a benchmark run, before it has been stored anywhere.
type CollectedArtifact struct {
	Benchmark db.BenchmarkKey
	Kind      string // e.g. "cpu.pprof"
	Data      []byte // raw artifact data
	Metadata  string // JSON string, e.g. {"perf_freq":997}
}

// RunAndCollect does everything Run() does except the DB writes:
// 1. Read git meta, build zig, run samples, parse/aggregate results -> ParsedRun
// 2. If profiling is enabled, capture CPU profiles -> []CollectedArtifact
// 3. Return both without touching any database
func RunAndCollect(ctx context.Context, cfg RunConfig) (*record.ParsedRun, []CollectedArtifact, error) {
	return RunAndCollectWithExecutor(ctx, cfg, OSRunner{})
}

// RunAndCollectWithExecutor exposes command injection for orchestration tests.
func RunAndCollectWithExecutor(ctx context.Context, cfg RunConfig, executor Executor) (*record.ParsedRun, []CollectedArtifact, error) {
	cfg = normalizeRunConfig(cfg)
	if err := validateRunConfig(cfg); err != nil {
		return nil, nil, err
	}
	if cfg.Samples < 1 {
		return nil, nil, fmt.Errorf("samples must be >= 1")
	}
	if cfg.Profile == ProfileCPU && cfg.PerfFreq <= 0 {
		cfg.PerfFreq = 997
	}

	meta, err := ReadGitMeta(ctx, cfg.RepoPath, executor)
	if err != nil {
		return nil, nil, fmt.Errorf("read git meta: %w", err)
	}

	if cfg.Branch != "" && meta.Branch == "" {
		meta.Branch = cfg.Branch
	}
	if cfg.Notes != "" {
		meta.Notes = cfg.Notes
	}
	if cfg.MachineID != "" {
		meta.MachineID = cfg.MachineID
	}
	meta.ZigOptimize = cfg.ZigOptimize
	meta.SampleCount = cfg.Samples
	meta.BenchmarkKind = string(cfg.BenchmarkKind)
	meta.BenchmarkSuite = cfg.BenchmarkSuite
	meta.ProtocolVersion = cfg.ProtocolVersion
	meta.BunVersion = cfg.BunVersion
	meta.JSRuntime = string(cfg.JSRuntime)
	meta.RuntimeVersion = cfg.RuntimeVersion
	meta.ZigVersion = cfg.ZigVersion
	meta.ManifestHash = cfg.ManifestHash

	if cfg.BenchmarkKind == BenchmarkJS {
		parsed, err := runJavaScript(ctx, cfg, meta, executor)
		return parsed, nil, err
	}

	zigDir := ZigDir(cfg.RepoPath)
	var args []string
	if cfg.Filter != "" {
		args = append(args, "--filter", cfg.Filter)
	}
	if cfg.FilterBenchmark != "" {
		args = append(args, "--bench", cfg.FilterBenchmark)
	}

	err = BuildZigBench(ctx, zigDir, cfg.ZigOptimize, executor)
	if err != nil {
		return nil, nil, fmt.Errorf("build failed: %w", err)
	}

	benchBin, err := FindBenchmarkBinary(zigDir)
	if err != nil {
		return nil, nil, fmt.Errorf("find benchmark binary: %w", err)
	}

	outputs := make([][]byte, 0, cfg.Samples)
	for i := 0; i < cfg.Samples; i++ {
		cmdArgs := []string{"--json", "--mem"}
		cmdArgs = append(cmdArgs, args...)

		cmd := exec.CommandContext(ctx, benchBin, cmdArgs...)
		cmd.Dir = zigDir
		out, err := executor.CombinedOutput(ctx, cmd)
		if err != nil {
			return nil, nil, fmt.Errorf("sample %d failed: %w", i+1, err)
		}

		outputs = append(outputs, out)
	}

	invocations := make([]io.Reader, len(outputs))
	for i := range outputs {
		invocations[i] = bytes.NewReader(outputs[i])
	}
	parsed, err := record.ParseInvocations(invocations, meta)
	if err != nil {
		return nil, nil, fmt.Errorf("parse results: %w", err)
	}

	var artifacts []CollectedArtifact

	if cfg.Profile == ProfileCPU {
		if cfg.ZigOptimize != "ReleaseSafe" {
			err = BuildZigBench(ctx, zigDir, "ReleaseSafe", executor)
			if err != nil {
				return parsed, nil, fmt.Errorf("profiling build failed: %w", err)
			}
			benchBin, err = FindBenchmarkBinary(zigDir)
			if err != nil {
				return parsed, nil, fmt.Errorf("find benchmark binary (safe): %w", err)
			}
		}

		for _, res := range parsed.Results {
			benchmark := db.BenchmarkKey{Category: res.Category, Name: res.Name}
			if !hasUniqueProfileSelector(parsed.Results, benchmark) {
				continue
			}

			pbGz, kind, err := CaptureCPUProfile(ctx, executor, benchBin, benchmark, cfg.PerfFreq)
			if err != nil {
				return parsed, artifacts, fmt.Errorf("profile %s/%s: %w", res.Category, res.Name, err)
			}

			artifacts = append(artifacts, CollectedArtifact{
				Benchmark: benchmark,
				Kind:      kind,
				Data:      pbGz,
				Metadata:  fmt.Sprintf(`{"perf_freq":%d}`, cfg.PerfFreq),
			})
		}
	}

	return parsed, artifacts, nil
}

func hasUniqueProfileSelector(results []record.ParsedResult, benchmark db.BenchmarkKey) bool {
	matches := 0
	for _, candidate := range results {
		if strings.Contains(strings.ToLower(candidate.Category), strings.ToLower(benchmark.Category)) &&
			strings.Contains(strings.ToLower(candidate.Name), strings.ToLower(benchmark.Name)) {
			matches++
		}
	}
	return matches == 1
}

func normalizeRunConfig(cfg RunConfig) RunConfig {
	if cfg.BenchmarkKind == "" {
		cfg.BenchmarkKind = BenchmarkZig
	}
	if cfg.BenchmarkSuite == "" {
		cfg.BenchmarkSuite = jsbench.Suite
	}
	if cfg.ProtocolVersion == 0 {
		cfg.ProtocolVersion = jsbench.Protocol
	}
	if cfg.BenchmarkKind == BenchmarkJS {
		if cfg.JSRuntime == "" {
			cfg.JSRuntime = RuntimeBun
		}
		if cfg.RuntimeVersion == "" {
			cfg.RuntimeVersion = jsbench.RuntimeVersion(string(cfg.JSRuntime))
		}
		if cfg.JSRuntime == RuntimeBun && cfg.BunVersion == "" {
			cfg.BunVersion = cfg.RuntimeVersion
		}
		if cfg.ZigVersion == "" {
			cfg.ZigVersion = jsbench.ZigVersion
		}
		if cfg.ManifestHash == "" {
			cfg.ManifestHash = jsbench.ManifestDigest
		}
	}
	return cfg
}

func validateRunConfig(cfg RunConfig) error {
	if cfg.BenchmarkKind != BenchmarkZig && cfg.BenchmarkKind != BenchmarkJS {
		return fmt.Errorf("benchmark kind must be zig or js")
	}
	if cfg.BenchmarkKind == BenchmarkZig && (cfg.BunVersion != "" || cfg.JSRuntime != "" || cfg.RuntimeVersion != "" ||
		cfg.ZigVersion != "" || cfg.ManifestHash != "") {
		return fmt.Errorf("Zig benchmarks must not include JavaScript identity")
	}
	if cfg.BenchmarkKind == BenchmarkJS {
		if !jsbench.MatchesIdentity(cfg.BenchmarkSuite, cfg.ProtocolVersion, string(cfg.JSRuntime), cfg.RuntimeVersion, cfg.ZigVersion, cfg.ManifestHash) {
			return fmt.Errorf("JavaScript benchmark identity is not canonical")
		}
		if cfg.Samples != jsbench.Samples || cfg.Profile != ProfileNone || cfg.Filter != "" ||
			cfg.FilterBenchmark != "" || len(cfg.Benchmarks) != 0 {
			return fmt.Errorf("JavaScript benchmarks require samples=3, profile=none, and no filters")
		}
	}
	return nil
}

// Run orchestrates the full benchmark pipeline: build, run, record, profile.
// It calls RunAndCollect() then stores results and artifacts in the local DB.
func Run(ctx context.Context, database *db.DB, cfg RunConfig) (int64, error) {
	parsed, artifacts, err := RunAndCollect(ctx, cfg)
	if err != nil {
		return 0, err
	}

	runID, _, err := record.Store(database, parsed)
	if err != nil {
		return 0, fmt.Errorf("store results: %w", err)
	}

	if len(artifacts) > 0 {
		results, err := database.GetResultsForRun(runID)
		if err != nil {
			return runID, fmt.Errorf("get results for artifacts: %w", err)
		}

		resultIDs := make(map[db.BenchmarkKey]int64, len(results))
		for _, res := range results {
			resultIDs[db.BenchmarkKey{Category: res.Category, Name: res.Name}] = res.ID
		}

		for _, art := range artifacts {
			resultID, ok := resultIDs[art.Benchmark]
			if !ok {
				return runID, fmt.Errorf("no result found for artifact benchmark %s/%s", art.Benchmark.Category, art.Benchmark.Name)
			}
			_, err = database.InsertArtifact(&db.Artifact{
				ResultID:  resultID,
				Kind:      art.Kind,
				DataBlob:  art.Data,
				Metadata:  art.Metadata,
				CreatedAt: time.Now().Format(time.RFC3339),
			})
			if err != nil {
				return runID, fmt.Errorf("store artifact for %s/%s: %w", art.Benchmark.Category, art.Benchmark.Name, err)
			}
		}
		if _, err := database.PruneProfileData(db.ProfileRetention{
			MaxRuns:  db.DefaultProfileRunsMax,
			MaxBytes: db.DefaultProfileBytesMax,
		}); err != nil {
			return runID, fmt.Errorf("prune profile data: %w", err)
		}
	}

	return runID, nil
}
