package runner

import (
	"context"
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
)

const fixtureZig = `#!/bin/sh
set -eu
if [ "$1" = version ]; then
  cat toolchain-version
  exit
fi
test "$(cat prepared)" = "$(cat toolchain-version)"
mkdir -p .fixture-bin
cp fixture-bench.sh .fixture-bin/opentui-bench
chmod +x .fixture-bin/opentui-bench
printf '%s/.fixture-bin/opentui-bench --help\n' "$PWD"
`

const fixtureBenchmark = `#!/bin/sh
set -eu
asset=$(cat ../examples/src/assets/image-demo.png)
if [ "$1" = --help ]; then
  printf 'Usage: --json --filter --bench --mem\n'
  exit
fi
printf '{"benchmark":"Image Operations","results":[{"name":"PNG","min_ns":%s,"avg_ns":%s,"max_ns":%s,"total_ns":%s,"iterations":1}]}\n' "$asset" "$asset" "$asset" "$asset"
`

type worktreeExecutor struct {
	t           *testing.T
	buildDirs   []string
	profileDirs []string
	failure     string
	cancel      context.CancelFunc
}

func (f *worktreeExecutor) Output(ctx context.Context, cmd *exec.Cmd) ([]byte, []byte, error) {
	out, err := f.CombinedOutput(ctx, cmd)
	return out, nil, err
}

func (f *worktreeExecutor) CombinedOutput(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	f.t.Helper()
	switch cmd.Args[0] {
	case "rm":
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > investigationCleanupTimeout || ctx.Err() != nil {
			f.t.Fatal("workspace cleanup must use its own bounded, uncancelled context")
		}
		for _, path := range cmd.Args[3:] {
			if !filepath.IsAbs(path) {
				f.t.Fatalf("workspace cleanup path must be absolute: %s", path)
			}
		}
	case "git":
		if len(cmd.Args) > 2 && cmd.Args[1] == "worktree" {
			out, err := (OSRunner{}).CombinedOutput(ctx, cmd)
			if err == nil && cmd.Args[2] == "add" && f.failure == "interrupted-add" && strings.Contains(cmd.Args[len(cmd.Args)-2], "target-source") {
				lock := exec.CommandContext(ctx, "git", "worktree", "lock", cmd.Args[len(cmd.Args)-2])
				lock.Dir = cmd.Dir
				if out, err := (OSRunner{}).CombinedOutput(ctx, lock); err != nil {
					f.t.Fatalf("lock interrupted worktree: %v\n%s", err, out)
				}
				f.cancel()
				return out, ctx.Err()
			}
			return out, err
		}
	case "zig":
		if cmd.Args[1] == "build" {
			f.buildDirs = append(f.buildDirs, cmd.Dir)
			if strings.Contains(cmd.Dir, "target-source") {
				switch f.failure {
				case "build":
					return nil, errors.New("fixture build failed")
				case "cancel-build":
					f.cancel()
					return nil, ctx.Err()
				}
			}
		}
		args := append([]string{filepath.Join(cmd.Dir, "fixture-zig.sh")}, cmd.Args[1:]...)
		script := exec.CommandContext(ctx, "sh", args...)
		script.Dir = cmd.Dir
		return (OSRunner{}).CombinedOutput(ctx, script)
	case "perf":
		if cmd.Args[1] == "script" {
			return []byte("bench 1 [001] 1.0: cycles:\n\tabcd decode_image+0x1 (bench)\n\n"), nil
		}
		f.profileDirs = append(f.profileDirs, cmd.Dir)
		if f.failure == "profile" && strings.Contains(cmd.Dir, "target-source") {
			return nil, errors.New("fixture profile failed")
		}
		separator := slices.Index(cmd.Args, "--")
		benchmark := exec.CommandContext(ctx, cmd.Args[separator+1], cmd.Args[separator+2:]...)
		benchmark.Dir = cmd.Dir
		return (OSRunner{}).CombinedOutput(ctx, benchmark)
	case "perf_to_profile":
		return nil, errors.New("use perf script fixture")
	default:
		if f.failure == "timing" && filepath.Base(cmd.Args[0]) == "target" && !slices.Contains(cmd.Args, "--help") {
			return nil, errors.New("fixture timing failed")
		}
	}
	return (OSRunner{}).CombinedOutput(ctx, cmd)
}

func investigationGitFixture(t *testing.T) PairConfig {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := t.TempDir()
	fixtureGit(t, repo, "init", "--quiet", "--template=", "--initial-branch=main")
	var commits []string
	for i, source := range []string{"packages/core/src/zig", "packages/native", "packages/native"} {
		if err := os.RemoveAll(filepath.Join(repo, "packages")); err != nil {
			t.Fatal(err)
		}
		files := map[string]string{
			"build.zig":                             "// fixture\n",
			"fixture-zig.sh":                        fixtureZig,
			"fixture-bench.sh":                      fixtureBenchmark,
			"toolchain-version":                     fmt.Sprintf("fixture-zig-%d\n", i),
			"scripts/prepare-zig-deps.sh":           "#!/bin/sh\nset -eu\ncat toolchain-version > prepared\n",
			"../examples/src/assets/image-demo.png": fmt.Sprintf("%d\n", (i+1)*100),
		}
		for path, data := range files {
			path = filepath.Join(repo, source, path)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		fixtureGit(t, repo, "add", "--all")
		fixtureGit(t, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "--quiet", "--no-gpg-sign", "-m", fmt.Sprintf("revision %d", i))
		commits = append(commits, fixtureGit(t, repo, "rev-parse", "HEAD"))
	}
	if err := os.WriteFile(filepath.Join(repo, "packages/examples/src/assets/image-demo.png"), []byte("999\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return PairConfig{
		RunConfig:      RunConfig{RepoPath: repo, WorkDir: t.TempDir(), Samples: 2, Profile: ProfileCPU, Branch: "main"},
		BaselineCommit: commits[0], TargetCommit: commits[1], CandidateCommit: commits[2],
		Category: "Image Operations", Name: "PNG", AttemptKey: "worktree-fixture",
	}
}

func fixtureGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func assertInvestigationWorktreesCleaned(t *testing.T, cfg PairConfig) {
	t.Helper()
	list := fixtureGit(t, cfg.RepoPath, "worktree", "list", "--porcelain")
	if strings.Count(list, "worktree ") != 1 || strings.Contains(list, cfg.WorkDir) {
		t.Fatalf("investigation worktree registration survived: %s", list)
	}
	entries, err := os.ReadDir(filepath.Join(cfg.RepoPath, ".git", "worktrees"))
	if (err != nil && !os.IsNotExist(err)) || len(entries) != 0 {
		t.Fatalf("worktree registrations remain: %v (%v)", entries, err)
	}
	assertNoInvestigationExecutions(t, cfg.WorkDir)
	if got := fixtureGit(t, cfg.RepoPath, "rev-parse", "HEAD"); got != cfg.CandidateCommit {
		t.Fatalf("original checkout changed to %s", got)
	}
	asset, err := os.ReadFile(filepath.Join(cfg.RepoPath, "packages/examples/src/assets/image-demo.png"))
	if err != nil || string(asset) != "999\n" {
		t.Fatalf("original working tree changed: %s (%v)", asset, err)
	}
}

func TestInvestigationUsesRevisionSpecificRuntimeAssets(t *testing.T) {
	cfg := investigationGitFixture(t)
	executor := &worktreeExecutor{t: t}
	result, err := RunInvestigation(context.Background(), cfg, executor)
	if err != nil {
		t.Fatal(err)
	}
	recording, err := result.Recording()
	if err != nil {
		t.Fatal(err)
	}
	var recipes map[string]Recipe
	if err := json.Unmarshal(recording.Recipe, &recipes); err != nil {
		t.Fatal(err)
	}
	for i, side := range recording.Runs {
		want := int64((i + 1) * 100)
		if got := side.Run.Results[0].AvgNs; got != want {
			t.Errorf("%s timing read another revision's asset: %d, want %d", side.Run.Meta.AttemptRole, got, want)
		}
		var profileMeta struct {
			TotalNs int64 `json:"total_ns"`
		}
		if err := json.Unmarshal([]byte(side.Artifacts[0].Metadata), &profileMeta); err != nil || profileMeta.TotalNs != want {
			t.Errorf("%s profile read another revision's asset: %+v (%v)", side.Run.Meta.AttemptRole, profileMeta, err)
		}
		recipe := recipes[side.Run.Meta.AttemptRole]
		if recipe.WorkingDir != executor.buildDirs[i] || recipe.WorkingDir != executor.profileDirs[i] || recipe.SourceDir != recipe.WorkingDir || recipe.ZigVersion != fmt.Sprintf("fixture-zig-%d", i) {
			t.Errorf("recipe does not describe its revision's build and execution context: %+v", recipe)
		}
		if len(recipe.BuildCommands) != 2 || recipe.BuildCommands[0][1] != filepath.Join(recipe.SourceDir, "scripts", "prepare-zig-deps.sh") {
			t.Errorf("dependency preparation ran outside the recorded source context: %+v", recipe)
		}
	}
	assertInvestigationWorktreesCleaned(t, cfg)
}

func TestInvestigationCleansWorktreesAfterFailureOrCancellation(t *testing.T) {
	for _, stage := range []string{"build", "timing", "profile", "cancel-build", "interrupted-add"} {
		t.Run(stage, func(t *testing.T) {
			cfg := investigationGitFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			executor := &worktreeExecutor{t: t, failure: stage, cancel: cancel}
			result, err := RunInvestigation(ctx, cfg, executor)
			if err == nil || result != nil {
				t.Fatalf("failure returned a measurement: %+v (%v)", result, err)
			}
			if ctx.Err() != nil && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation was lost: %v", err)
			}
			assertInvestigationWorktreesCleaned(t, cfg)
		})
	}
}
