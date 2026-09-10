package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"opentui-bench/internal/db"
	"opentui-bench/internal/record"
)

const selectedJSON = `{"benchmark":"Terminal Image","results":[{"name":"flat","min_ns":90,"avg_ns":100,"max_ns":110,"total_ns":1000,"iterations":10}]}`

type investigationExecutor struct {
	t             *testing.T
	dir           string
	current       string
	currentPath   string
	refs          map[string]string
	checkouts     []string
	commands      int
	timing        []string
	profiles      []string
	timingOutputs map[int]string
	profileOutput *string
}

func newInvestigationExecutor(t *testing.T) (*investigationExecutor, PairConfig) {
	t.Helper()
	fake := &investigationExecutor{
		t: t, dir: t.TempDir(), refs: map[string]string{
			"baseline-ref":  strings.Repeat("a", 40),
			"target-ref":    strings.Repeat("b", 40),
			"candidate-ref": strings.Repeat("c", 40),
		},
	}
	if err := os.Mkdir(filepath.Join(fake.dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return fake, PairConfig{
		RunConfig:      RunConfig{RepoPath: fake.dir, Samples: 3, Profile: ProfileCPU, Branch: "main", WorkDir: t.TempDir()},
		BaselineCommit: "baseline-ref", TargetCommit: "target-ref", Category: "Terminal Image", Name: "flat", AttemptKey: "test-pair",
	}
}

func (f *investigationExecutor) Output(ctx context.Context, cmd *exec.Cmd) ([]byte, []byte, error) {
	out, err := f.CombinedOutput(ctx, cmd)
	return out, nil, err
}

func (f *investigationExecutor) CombinedOutput(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	f.t.Helper()
	f.commands++
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > MaxInvestigationDuration {
		f.t.Fatal("investigation command has no bounded deadline")
	}
	switch cmd.Args[0] {
	case "rm":
		return (OSRunner{}).CombinedOutput(ctx, cmd)
	case "git":
		if slices.Equal(cmd.Args[1:], []string{"rev-parse", "--path-format=absolute", "--git-common-dir"}) {
			return []byte(filepath.Join(f.dir, ".git")), nil
		}
		if cmd.Args[1] == "rev-parse" && cmd.Args[2] == "--verify" {
			ref := strings.TrimSuffix(cmd.Args[len(cmd.Args)-1], "^{commit}")
			if commit := f.refs[ref]; commit != "" {
				return []byte(commit), nil
			}
			return nil, fmt.Errorf("unknown ref %q", ref)
		}
		if cmd.Args[1] == "worktree" {
			f.currentPath = cmd.Args[len(cmd.Args)-2]
			f.current = cmd.Args[len(cmd.Args)-1]
			f.checkouts = append(f.checkouts, f.currentPath)
			dir := ZigDir(f.currentPath)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, err
			}
			return nil, os.WriteFile(filepath.Join(dir, "build.zig"), nil, 0o644)
		}
		if cmd.Dir != f.currentPath {
			f.t.Fatalf("metadata read outside revision worktree: %s", cmd.Dir)
		}
		switch strings.Join(cmd.Args[1:], " ") {
		case "rev-parse --short HEAD":
			return []byte(f.current[:8]), nil
		case "rev-parse HEAD":
			return []byte(f.current), nil
		case "log -1 --format=%s":
			return []byte("commit " + f.current[:8]), nil
		case "log -1 --format=%cI":
			return []byte("2026-09-09T01:02:03Z"), nil
		case "branch --show-current":
			return nil, nil
		}
	case "zig":
		if cmd.Args[1] == "version" {
			if cmd.Dir != ZigDir(f.currentPath) {
				f.t.Fatalf("Zig version directory = %s", cmd.Dir)
			}
			return []byte("zig-for-" + f.current[:8]), nil
		}
		if !slices.Contains(cmd.Args, "-Dbench-optimize=ReleaseFast") {
			f.t.Fatalf("unoptimized build: %v", cmd.Args)
		}
		path := filepath.Join(cmd.Dir, "opentui-bench")
		if err := os.WriteFile(path, []byte(f.current), 0o755); err != nil {
			return nil, err
		}
		return []byte(path + " --help\n"), nil
	case "perf":
		if cmd.Args[1] == "script" {
			return []byte("bench 1 [001] 1.0: cycles:\n\tabcd measured_func+0x1 (bench)\n\n"), nil
		}
		separator := slices.Index(cmd.Args, "--")
		path := cmd.Args[separator+1]
		f.verifyExecutable(path, cmd.Dir)
		f.profiles = append(f.profiles, path)
		if !slices.Contains(cmd.Args, "--mem") {
			f.t.Fatal("investigation profile omitted timing workload's --mem flag")
		}
		if f.profileOutput != nil {
			return []byte(*f.profileOutput), nil
		}
		return []byte(selectedJSON + "\n[ perf record: captured samples ]\n"), nil
	case "perf_to_profile":
		return nil, errors.New("exercise perf script fallback")
	default:
		if slices.Equal(cmd.Args[1:], []string{"--help"}) {
			return []byte("Usage: --json --filter --bench --mem"), nil
		}
		f.verifyExecutable(cmd.Args[0], cmd.Dir)
		f.timing = append(f.timing, cmd.Args[0])
		if out, ok := f.timingOutputs[len(f.timing)]; ok {
			return []byte(out), nil
		}
		return []byte(selectedJSON), nil
	}
	return nil, fmt.Errorf("unexpected command %v", cmd.Args)
}

func (f *investigationExecutor) verifyExecutable(path, workingDir string) {
	f.t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		f.t.Fatal(err)
	}
	want := f.refs[filepath.Base(path)+"-ref"]
	workspace := investigationWorkspace{path: filepath.Dir(path)}
	if workingDir != ZigDir(workspace.worktreePath(filepath.Base(path))) {
		f.t.Fatalf("binary %s executed outside its own source context: %s", path, workingDir)
	}
	if string(data) != want {
		f.t.Fatalf("%s executes %q, want copied revision %s", path, data, want)
	}
}

func TestInvestigationAlternatesCopiedRevisions(t *testing.T) {
	for _, candidate := range []bool{false, true} {
		t.Run(fmt.Sprintf("candidate=%t", candidate), func(t *testing.T) {
			fake, cfg := newInvestigationExecutor(t)
			if candidate {
				cfg.CandidateCommit = "candidate-ref"
			}
			result, err := RunInvestigation(context.Background(), cfg, fake)
			if err != nil {
				t.Fatal(err)
			}
			wantOrder := []string{"baseline", "target", "target", "baseline", "baseline", "target"}
			if candidate {
				wantOrder = []string{"baseline", "target", "candidate", "candidate", "target", "baseline", "baseline", "target", "candidate"}
			}
			var order []string
			for _, path := range fake.timing {
				order = append(order, filepath.Base(path))
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("temporary binary survived: %s (%v)", path, err)
				}
			}
			if !slices.Equal(order, wantOrder) {
				t.Fatalf("order = %v, want %v", order, wantOrder)
			}
			recording, err := result.Recording()
			if err != nil {
				t.Fatal(err)
			}
			var recipes map[string]Recipe
			if err := json.Unmarshal(recording.Recipe, &recipes); err != nil {
				t.Fatal(err)
			}
			if len(recipes) != len(recording.Runs) {
				t.Fatalf("recipes = %d, runs = %d", len(recipes), len(recording.Runs))
			}
			hostname, _ := os.Hostname()
			for _, side := range recording.Runs {
				meta := side.Run.Meta
				want := fake.refs[meta.AttemptRole+"-ref"]
				if meta.CommitHashFull != want || meta.CommitHash != want[:8] || meta.MachineID != hostname || meta.CommitDate == "" || meta.CommitMessage == "" || meta.Branch != "main" {
					t.Fatalf("metadata not read from checkout: %+v", meta)
				}
				recipe := recipes[meta.AttemptRole]
				hash := sha256.Sum256([]byte(want))
				if recipe.CommitHash != want || recipe.ZigVersion != "zig-for-"+want[:8] || meta.ZigVersion != recipe.ZigVersion || recipe.ExecutableSHA256 != hex.EncodeToString(hash[:]) {
					t.Fatalf("missing revision provenance: %+v", recipe)
				}
				if len(recipe.BuildCommands) == 0 || !slices.Contains(recipe.BuildCommands[0], "-Dbench-optimize=ReleaseFast") || !slices.Contains(fake.profiles, recipe.TimingCommand[0]) || !slices.Contains(recipe.ProfileCommand, recipe.TimingCommand[0]) {
					t.Fatalf("commands do not describe captured executable: %+v", recipe)
				}
				if len(side.Artifacts) != 1 || len(side.Run.Results[0].Samples) != 3 {
					t.Fatalf("incomplete measurement: %+v", side)
				}
			}
		})
	}
}

func TestInvestigationUnchangedTargetCandidateHasFreshControls(t *testing.T) {
	fake, cfg := newInvestigationExecutor(t)
	cfg.CandidateCommit = "candidate-ref"
	fake.refs["candidate-ref"] = fake.refs["target-ref"]
	result, err := RunInvestigation(context.Background(), cfg, fake)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.timing) != 9 || result.Baseline == nil || result.Target == nil || result.Candidate == nil {
		t.Fatalf("candidate did not remeasure all controls: %+v", result)
	}
	if result.Target.Meta.CommitHashFull != result.Candidate.Meta.CommitHashFull || result.Target.Meta.AttemptRole == result.Candidate.Meta.AttemptRole || result.TargetRecipe.ExecutableSHA256 != result.CandidateRecipe.ExecutableSHA256 {
		t.Fatal("unchanged target candidate must preserve the revision while recording distinct roles")
	}
}

func TestInvestigationRejectsActualInvocationMismatch(t *testing.T) {
	for name, out := range map[string]string{
		"substring collision": selectedJSON + "\n" + strings.Replace(selectedJSON, `"flat"`, `"flat count-only"`, 1),
		"wrong single result": strings.Replace(selectedJSON, `"flat"`, `"another"`, 1),
		"missing invocation":  "Memory stats enabled\n",
		"case mismatch":       strings.Replace(selectedJSON, "Terminal Image", "terminal image", 1),
		"duplicate result":    selectedJSON + "\n" + selectedJSON,
	} {
		t.Run(name, func(t *testing.T) {
			fake, cfg := newInvestigationExecutor(t)
			fake.timingOutputs = map[int]string{3: out}
			result, err := RunInvestigation(context.Background(), cfg, fake)
			if result != nil || !errors.Is(err, ErrMismatchedWorkload) {
				t.Fatalf("result = %v, err = %v", result, err)
			}
			if len(fake.timing) != 3 || len(fake.profiles) != 0 {
				t.Fatal("execution continued after invalid sample")
			}
			assertNoInvestigationExecutions(t, cfg.WorkDir)
		})
	}
}

func TestInvestigationValidatesProfileInvocation(t *testing.T) {
	fake, cfg := newInvestigationExecutor(t)
	out := selectedJSON + "\n" + strings.Replace(selectedJSON, `"flat"`, `"flat count-only"`, 1)
	fake.profileOutput = &out
	result, err := RunInvestigation(context.Background(), cfg, fake)
	if result != nil || !errors.Is(err, ErrMismatchedWorkload) || len(fake.profiles) != 1 {
		t.Fatalf("profile mismatch accepted: result = %v, err = %v", result, err)
	}
}

func TestInvestigationSampleBoundBeforeCheckout(t *testing.T) {
	for _, samples := range []int{-1, 0, db.MaxInvestigationSamples + 1} {
		fake, cfg := newInvestigationExecutor(t)
		cfg.Samples = samples
		if _, err := RunInvestigation(context.Background(), cfg, fake); err == nil {
			t.Fatalf("accepted %d samples", samples)
		}
		if len(fake.checkouts) != 0 || fake.commands != 0 {
			t.Fatal("invalid sample count reached checkout/build")
		}
	}
}

func TestInvestigationLocalRetryPreservesOriginalSnapshot(t *testing.T) {
	fake, cfg := newInvestigationExecutor(t)
	result, err := RunInvestigation(context.Background(), cfg, fake)
	if err != nil {
		t.Fatal(err)
	}
	recording, err := result.Recording()
	if err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(filepath.Join(t.TempDir(), "bench.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	_, _, _, _, err = database.CreateInvestigationIfAbsent(db.InvestigationCreate{
		Category: cfg.Category, Name: cfg.Name, BaselineCommit: result.Baseline.Meta.CommitHashFull,
		TargetCommit: result.Target.Meta.CommitHashFull, AttemptKey: cfg.AttemptKey, Samples: cfg.Samples, Profile: "cpu",
	})
	if err != nil {
		t.Fatal(err)
	}
	retention := db.ProfileRetention{MaxRuns: 10, MaxBytes: 32 << 20}
	attempt, created, err := record.StoreInvestigation(database, recording, retention)
	if err != nil || !created || attempt.RunID == 0 || attempt.BaselineRunID == 0 || attempt.TargetRunID == 0 {
		t.Fatalf("attempt = %+v, created = %t, err = %v", attempt, created, err)
	}
	originalRecipe := string(recording.Recipe)
	originalProfile := string(recording.Runs[0].Artifacts[0].Data)
	recording.Runs[0].Artifacts[0].Data = []byte("different generation")
	recording.Runs[0].Run.Results[0].AvgNs = 200
	recording.Runs[0].Run.Meta.ZigVersion = "another-toolchain"
	recording.Recipe = []byte(strings.Replace(originalRecipe, "zig-for-aaaaaaaa", "another-toolchain", 1))
	retry, created, err := record.StoreInvestigation(database, recording, retention)
	if err != nil || created || retry.RunID != attempt.RunID || retry.RecipeJSON != originalRecipe {
		t.Fatalf("retry = %+v, created = %t, err = %v", retry, created, err)
	}
	var runs, artifacts int
	if err := database.QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM artifacts`).Scan(&artifacts); err != nil {
		t.Fatal(err)
	}
	if runs != 2 || artifacts != 2 {
		t.Fatalf("retry duplicated local artifacts or runs: %d runs, %d artifacts", runs, artifacts)
	}
	stored, err := database.GetResultsForRun(attempt.BaselineRunID)
	if err != nil || len(stored) != 1 || stored[0].AvgNs != 100 {
		t.Fatalf("original timing changed: %+v (%v)", stored, err)
	}
	artifact, err := database.GetArtifact(stored[0].ID, "cpu.pprof")
	if err != nil || string(artifact.DataBlob) != originalProfile {
		t.Fatalf("original profile changed: %+v (%v)", artifact, err)
	}
}
