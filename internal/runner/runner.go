package runner

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"

	"opentui-bench/internal/db"
	"opentui-bench/internal/record"
)

type ProfileMode string

const (
	ProfileNone ProfileMode = "none"
	ProfileCPU  ProfileMode = "cpu"
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
}

// CollectedArtifact holds a binary artifact (e.g., pprof profile) captured
// during a benchmark run, before it has been stored anywhere.
type CollectedArtifact struct {
	BenchmarkName string // benchmark name this artifact belongs to
	Kind          string // e.g. "cpu.pprof"
	Data          []byte // raw artifact data
	Metadata      string // JSON string, e.g. {"perf_freq":997}
}

// RunAndCollect does everything Run() does except the DB writes:
// 1. Read git meta, build zig, run samples, parse/aggregate results -> ParsedRun
// 2. If profiling is enabled, capture CPU profiles -> []CollectedArtifact
// 3. Return both without touching any database
func RunAndCollect(ctx context.Context, cfg RunConfig) (*record.ParsedRun, []CollectedArtifact, error) {
	if cfg.Samples < 1 {
		return nil, nil, fmt.Errorf("samples must be >= 1")
	}
	if cfg.Profile == ProfileCPU && cfg.PerfFreq <= 0 {
		cfg.PerfFreq = 997
	}

	runner := OSRunner{}

	meta, err := ReadGitMeta(ctx, cfg.RepoPath, runner)
	if err != nil {
		return nil, nil, fmt.Errorf("read git meta: %w", err)
	}

	if cfg.Notes != "" {
		meta.Notes = cfg.Notes
	}
	if cfg.MachineID != "" {
		meta.MachineID = cfg.MachineID
	}
	meta.ZigOptimize = cfg.ZigOptimize
	meta.SampleCount = cfg.Samples

	zigDir := ZigDir(cfg.RepoPath)
	var args []string
	if cfg.Filter != "" {
		args = append(args, "--filter", cfg.Filter)
	}
	if cfg.FilterBenchmark != "" {
		args = append(args, "--bench", cfg.FilterBenchmark)
	}

	err = BuildZigBench(ctx, zigDir, cfg.ZigOptimize, runner)
	if err != nil {
		return nil, nil, fmt.Errorf("build failed: %w", err)
	}

	benchBin, err := FindBenchmarkBinary(zigDir)
	if err != nil {
		return nil, nil, fmt.Errorf("find benchmark binary: %w", err)
	}

	var buf bytes.Buffer
	for i := 0; i < cfg.Samples; i++ {
		cmdArgs := []string{"--json", "--mem"}
		cmdArgs = append(cmdArgs, args...)

		cmd := exec.CommandContext(ctx, benchBin, cmdArgs...)
		cmd.Dir = zigDir
		out, err := runner.CombinedOutput(ctx, cmd)
		if err != nil {
			return nil, nil, fmt.Errorf("sample %d failed: %w", i+1, err)
		}

		buf.Write(out)
		if len(out) > 0 && out[len(out)-1] != '\n' {
			buf.WriteByte('\n')
		}
	}

	parsed, err := record.Parse(bytes.NewReader(buf.Bytes()), meta)
	if err != nil {
		return nil, nil, fmt.Errorf("parse results: %w", err)
	}

	var artifacts []CollectedArtifact

	if cfg.Profile == ProfileCPU {
		if cfg.ZigOptimize != "ReleaseSafe" {
			err = BuildZigBench(ctx, zigDir, "ReleaseSafe", runner)
			if err != nil {
				return parsed, nil, fmt.Errorf("profiling build failed: %w", err)
			}
			benchBin, err = FindBenchmarkBinary(zigDir)
			if err != nil {
				return parsed, nil, fmt.Errorf("find benchmark binary (safe): %w", err)
			}
		}

		for _, res := range parsed.Results {
			pbGz, kind, err := CaptureCPUProfile(ctx, runner, benchBin, res.Name, cfg.PerfFreq)
			if err != nil {
				return parsed, artifacts, fmt.Errorf("profile %s: %w", res.Name, err)
			}

			artifacts = append(artifacts, CollectedArtifact{
				BenchmarkName: res.Name,
				Kind:          kind,
				Data:          pbGz,
				Metadata:      fmt.Sprintf(`{"perf_freq":%d}`, cfg.PerfFreq),
			})
		}
	}

	return parsed, artifacts, nil
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

		// Build a map from benchmark name to result ID
		nameToResultID := make(map[string]int64, len(results))
		for _, res := range results {
			nameToResultID[res.Name] = res.ID
		}

		for _, art := range artifacts {
			resultID, ok := nameToResultID[art.BenchmarkName]
			if !ok {
				return runID, fmt.Errorf("no result found for artifact benchmark %s", art.BenchmarkName)
			}
			_, err = database.InsertArtifact(&db.Artifact{
				ResultID:  resultID,
				Kind:      art.Kind,
				DataBlob:  art.Data,
				Metadata:  art.Metadata,
				CreatedAt: time.Now().Format(time.RFC3339),
			})
			if err != nil {
				return runID, fmt.Errorf("store artifact for %s: %w", art.BenchmarkName, err)
			}
		}
	}

	return runID, nil
}
