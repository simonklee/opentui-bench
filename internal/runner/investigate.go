package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"opentui-bench/internal/db"
	"opentui-bench/internal/record"
)

const MaxInvestigationDuration = 30 * time.Minute

const investigationCleanupTimeout = 30 * time.Second

type PairConfig struct {
	RunConfig
	BaselineCommit  string
	TargetCommit    string
	CandidateCommit string
	Category        string
	Name            string
	AttemptKey      string
	Role            string
}

type PairResult struct {
	Baseline        *record.ParsedRun
	Target          *record.ParsedRun
	Candidate       *record.ParsedRun
	BaselineArts    []CollectedArtifact
	TargetArts      []CollectedArtifact
	CandidateArts   []CollectedArtifact
	BaselineRecipe  Recipe
	TargetRecipe    Recipe
	CandidateRecipe Recipe
}

func (result *PairResult) Recording() (record.InvestigationRecording, error) {
	var recording record.InvestigationRecording
	recipes := make(map[string]Recipe)
	for _, side := range []struct {
		run       *record.ParsedRun
		artifacts []CollectedArtifact
		recipe    Recipe
	}{
		{result.Baseline, result.BaselineArts, result.BaselineRecipe},
		{result.Target, result.TargetArts, result.TargetRecipe},
		{result.Candidate, result.CandidateArts, result.CandidateRecipe},
	} {
		if side.run == nil {
			continue
		}
		if recording.AttemptKey == "" {
			recording.AttemptKey = side.run.Meta.AttemptKey
		}
		run := record.InvestigationRun{Run: *side.run}
		for _, artifact := range side.artifacts {
			run.Artifacts = append(run.Artifacts, record.InvestigationArtifact{
				Kind: artifact.Kind, Data: artifact.Data, Metadata: artifact.Metadata,
			})
		}
		recording.Runs = append(recording.Runs, run)
		recipes[side.run.Meta.AttemptRole] = side.recipe
	}
	var err error
	recording.Recipe, err = json.Marshal(recipes)
	if err != nil {
		return recording, err
	}
	return recording, recording.Validate()
}

type investigationRevision struct {
	role       string
	commit     string
	bin        string
	worktree   string
	workingDir string
	meta       record.RunMetadata
	recipe     Recipe
	outputs    [][]byte
	parsed     *record.ParsedRun
	artifacts  []CollectedArtifact
}

func RunInvestigation(ctx context.Context, cfg PairConfig, executor Executor) (result *PairResult, err error) {
	cfg.RunConfig = normalizeRunConfig(cfg.RunConfig)
	if cfg.BenchmarkKind != BenchmarkZig {
		return nil, fmt.Errorf("%w: zig only", ErrUnsupportedHarness)
	}
	if cfg.Category == "" || cfg.Name == "" {
		return nil, fmt.Errorf("%w: category and name are required", ErrUnsupportedHarness)
	}
	if cfg.Samples < 1 || cfg.Samples > db.MaxInvestigationSamples {
		return nil, fmt.Errorf("samples must be between 1 and %d", db.MaxInvestigationSamples)
	}
	if err := db.ValidateAttemptKey(cfg.AttemptKey); err != nil {
		return nil, err
	}
	if cfg.BaselineCommit == "" || cfg.TargetCommit == "" {
		return nil, fmt.Errorf("baseline and target commits are required")
	}
	if cfg.Role == "" {
		cfg.Role = db.AttemptRolePair
		if cfg.CandidateCommit != "" {
			cfg.Role = db.AttemptRoleCandidate
		}
	}
	if cfg.Role != db.AttemptRolePair && cfg.Role != db.AttemptRoleCandidate {
		return nil, fmt.Errorf("unsupported investigation role %q", cfg.Role)
	}
	if (cfg.Role == db.AttemptRoleCandidate) != (cfg.CandidateCommit != "") {
		return nil, fmt.Errorf("candidate attempts require a candidate commit in addition to baseline and target")
	}
	if cfg.ZigOptimize == "" {
		cfg.ZigOptimize = "ReleaseFast"
	}
	if cfg.ZigOptimize != "ReleaseFast" && cfg.ZigOptimize != "ReleaseSafe" && cfg.ZigOptimize != "ReleaseSmall" {
		return nil, fmt.Errorf("investigations require an optimized Zig build")
	}
	if cfg.Profile == "" {
		cfg.Profile = ProfileNone
	}
	if cfg.Profile != ProfileNone && cfg.Profile != ProfileCPU {
		return nil, fmt.Errorf("profile must be none or cpu")
	}
	if cfg.PerfFreq <= 0 {
		cfg.PerfFreq = 997
	}
	ctx, cancel := context.WithTimeout(ctx, MaxInvestigationDuration)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	workspace, err := openInvestigationWorkspace(ctx, cfg.RepoPath, cfg.WorkDir, executor, true)
	if err != nil {
		return nil, err
	}

	sides := []investigationRevision{
		{role: db.AttemptRoleBaseline, commit: cfg.BaselineCommit},
		{role: db.AttemptRoleTarget, commit: cfg.TargetCommit},
	}
	if cfg.CandidateCommit != "" {
		sides = append(sides, investigationRevision{role: db.AttemptRoleCandidate, commit: cfg.CandidateCommit})
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), investigationCleanupTimeout)
		defer cleanupCancel()
		defer workspace.lease.Close()
		if cleanupErr := workspace.remove(cleanupCtx, executor); cleanupErr != nil {
			result = nil
			err = errors.Join(err, cleanupErr)
		}
	}()
	for i := range sides {
		if err := buildRevision(ctx, cfg, &sides[i], workspace, executor); err != nil {
			return nil, fmt.Errorf("%s: %w", sides[i].role, err)
		}
		sides[i].outputs = make([][]byte, cfg.Samples)
	}
	for sample := 0; sample < cfg.Samples; sample++ {
		for position := range sides {
			index := position
			if sample%2 == 1 {
				index = len(sides) - 1 - position
			}
			side := &sides[index]
			out, err := runSelected(ctx, executor, side.bin, side.workingDir, cfg)
			if err != nil {
				return nil, fmt.Errorf("%s sample %d: %w", side.role, sample+1, err)
			}
			side.outputs[sample] = out
		}
	}
	for i := range sides {
		side := &sides[i]
		invocations := make([]io.Reader, len(side.outputs))
		for j, out := range side.outputs {
			invocations[j] = bytes.NewReader(out)
		}
		side.parsed, err = record.ParseInvocations(invocations, side.meta)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", side.role, err)
		}
		if cfg.Profile == ProfileCPU {
			side.artifacts, err = profileSelected(ctx, executor, side.bin, side.workingDir, cfg)
			if err != nil {
				return nil, fmt.Errorf("%s profile: %w", side.role, err)
			}
		}
	}
	result = &PairResult{
		Baseline: sides[0].parsed, BaselineArts: sides[0].artifacts, BaselineRecipe: sides[0].recipe,
		Target: sides[1].parsed, TargetArts: sides[1].artifacts, TargetRecipe: sides[1].recipe,
	}
	if len(sides) == 3 {
		result.Candidate, result.CandidateArts, result.CandidateRecipe = sides[2].parsed, sides[2].artifacts, sides[2].recipe
	}
	return result, nil
}

func buildRevision(ctx context.Context, cfg PairConfig, side *investigationRevision, workspace *investigationWorkspace, executor Executor) error {
	resolve := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "--end-of-options", side.commit+"^{commit}")
	resolve.Dir = cfg.RepoPath
	resolved, err := executor.CombinedOutput(ctx, resolve)
	if err != nil {
		return fmt.Errorf("resolve %s: %w\n%s", side.commit, err, bytes.TrimSpace(resolved))
	}
	commit := strings.TrimSpace(string(resolved))
	if _, err := hex.DecodeString(commit); err != nil || (len(commit) != 40 && len(commit) != 64) {
		return fmt.Errorf("revision did not resolve to a full Git commit hash")
	}
	side.worktree = workspace.worktreePath(side.role)
	registration := filepath.Join(workspace.commonDir, "worktrees", filepath.Base(side.worktree))
	if _, err := os.Lstat(registration); !os.IsNotExist(err) {
		return fmt.Errorf("worktree registration already exists or cannot be inspected: %s (%v)", registration, err)
	}
	add := exec.CommandContext(ctx, "git", "worktree", "add", "--detach", side.worktree, commit)
	add.Dir = cfg.RepoPath
	if out, err := executor.CombinedOutput(ctx, add); err != nil {
		return fmt.Errorf("create worktree for %s: %w\n%s", commit, err, bytes.TrimSpace(out))
	}
	meta, err := ReadGitMeta(ctx, side.worktree, executor)
	if err != nil {
		return err
	}
	if meta.CommitHashFull != commit {
		return fmt.Errorf("worktree commit %s does not match resolved commit %s", meta.CommitHashFull, commit)
	}
	zigDir := ZigDir(side.worktree)
	side.workingDir = zigDir
	versionCmd := exec.CommandContext(ctx, "zig", "version")
	versionCmd.Dir = zigDir
	version, stderr, err := executor.Output(ctx, versionCmd)
	cleanedVersion := db.CleanZigVersion(string(version))
	if err != nil || cleanedVersion == "" {
		if msg := strings.TrimSpace(string(stderr)); msg != "" {
			return fmt.Errorf("read effective Zig version: %v\n%s", err, msg)
		}
		return fmt.Errorf("read effective Zig version: %v", err)
	}
	meta.ZigVersion = cleanedVersion
	benchBin, err := BuildZigBench(ctx, zigDir, cfg.ZigOptimize, executor)
	if err != nil {
		return err
	}
	capability, err := ProbeHarness(ctx, benchBin, zigDir, executor)
	if err != nil {
		return err
	}
	if err := capability.CanIsolate(cfg.Category, cfg.Name); err != nil {
		return err
	}
	side.bin = filepath.Join(workspace.path, side.role)
	digest, err := copyExecutable(benchBin, side.bin)
	if err != nil {
		return err
	}
	if meta.Branch == "" {
		meta.Branch = cfg.Branch
	}
	if cfg.MachineID != "" {
		meta.MachineID = cfg.MachineID
	}
	meta.Notes = cfg.Notes
	meta.ZigOptimize = cfg.ZigOptimize
	meta.SampleCount = cfg.Samples
	meta.BenchmarkKind = string(cfg.BenchmarkKind)
	meta.BenchmarkSuite = cfg.BenchmarkSuite
	meta.ProtocolVersion = cfg.ProtocolVersion
	meta.Purpose = db.PurposeInvestigation
	meta.AttemptKey = cfg.AttemptKey
	meta.AttemptRole = side.role
	side.meta = meta
	recipe := Recipe{
		CommitHash: meta.CommitHashFull, ExecutableSHA256: digest,
		ZigOptimize: cfg.ZigOptimize, SourceDir: zigDir, WorkingDir: zigDir,
		Harness: capability.HarnessName(), Filter: cfg.Category, Bench: cfg.Name,
		Samples: cfg.Samples, Profile: string(cfg.Profile), PerfFreq: cfg.PerfFreq,
		CaptureScope: "whole-process", ZigVersion: meta.ZigVersion, SelectorUnique: true,
		TimingCommand: append([]string{side.bin}, selectedArgs(cfg)...),
	}
	prepareScript := filepath.Join(zigDir, "scripts", "prepare-zig-deps.sh")
	if _, err := os.Stat(prepareScript); err == nil {
		recipe.BuildCommands = append(recipe.BuildCommands, []string{"sh", prepareScript})
	}
	recipe.BuildCommands = append(recipe.BuildCommands, []string{"zig", "build", "bench", "-Dbench-optimize=" + cfg.ZigOptimize, "--verbose", "--", "--help"})
	if cfg.Profile == ProfileCPU {
		recipe.ProfileCommand = append([]string{"perf", "record", "-F", strconv.Itoa(cfg.PerfFreq), "-g", "-o", "$PERF_DATA", "--"}, recipe.TimingCommand...)
	}
	side.recipe = recipe
	return nil
}

func selectedArgs(cfg PairConfig) []string {
	return []string{"--json", "--mem", "--filter", cfg.Category, "--bench", cfg.Name}
}

func validateSelectedOutput(out []byte, cfg PairConfig) (*record.ParsedResult, error) {
	parsed, err := record.Parse(bytes.NewReader(out), record.RunMetadata{})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMismatchedWorkload, err)
	}
	selected, err := selectedWorkload(parsed, cfg.Category, cfg.Name)
	if err != nil {
		return nil, err
	}
	if selected.Iterations <= 0 || selected.AvgNs <= 0 {
		return nil, fmt.Errorf("%w: workload has no timed operations", ErrMismatchedWorkload)
	}
	return selected, nil
}

func runSelected(ctx context.Context, executor Executor, benchBin, workingDir string, cfg PairConfig) ([]byte, error) {
	cmd := exec.CommandContext(ctx, benchBin, selectedArgs(cfg)...)
	cmd.Dir = workingDir
	out, err := executor.CombinedOutput(ctx, cmd)
	if err != nil {
		return nil, fmt.Errorf("run %s/%s: %w\n%s", cfg.Category, cfg.Name, err, strings.TrimSpace(string(out)))
	}
	if _, err := validateSelectedOutput(out, cfg); err != nil {
		return nil, err
	}
	return out, nil
}

func profileSelected(ctx context.Context, executor Executor, benchBin, workingDir string, cfg PairConfig) ([]CollectedArtifact, error) {
	var selected *record.ParsedResult
	data, kind, err := captureCPUProfile(ctx, executor, benchBin, workingDir, selectedArgs(cfg), cfg.PerfFreq, func(out []byte) error {
		var err error
		selected, err = validateSelectedOutput(out, cfg)
		return err
	})
	if err != nil {
		return nil, err
	}
	metadata, err := json.Marshal(struct {
		PerfFreq     int    `json:"perf_freq"`
		CaptureScope string `json:"capture_scope"`
		Iterations   int64  `json:"iterations"`
		TotalNs      int64  `json:"total_ns"`
	}{cfg.PerfFreq, "whole-process", selected.Iterations, selected.TotalNs})
	if err != nil {
		return nil, err
	}
	return []CollectedArtifact{{
		Benchmark: db.BenchmarkKey{Category: cfg.Category, Name: cfg.Name},
		Kind:      kind, Data: data, Metadata: string(metadata),
	}}, nil
}

func copyExecutable(source, destination string) (string, error) {
	in, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return "", err
	}
	defer func() { _ = out.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, hash), in); err != nil {
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
