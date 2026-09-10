package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func assertNoInvestigationExecutions(t *testing.T, workDir string) {
	t.Helper()
	roots, err := os.ReadDir(workDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range roots {
		entries, err := os.ReadDir(filepath.Join(workDir, root.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.Name() != ".lock" && entry.Name() != ".owner" {
				t.Fatalf("execution survived in %s: %s", root.Name(), entry.Name())
			}
		}
	}
}

func newTestWorkspace(t *testing.T, cfg PairConfig) *investigationWorkspace {
	t.Helper()
	workspace, err := openInvestigationWorkspace(context.Background(), cfg.RepoPath, cfg.WorkDir, OSRunner{}, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.lease.Close() })
	return workspace
}

func addTestWorkspaceWorktree(t *testing.T, workspace *investigationWorkspace) string {
	t.Helper()
	path := workspace.worktreePath("baseline")
	fixtureGit(t, workspace.repoPath, "worktree", "add", "--detach", path, "HEAD")
	fixtureGit(t, workspace.repoPath, "worktree", "lock", "--reason", "initializing", path)
	cache := filepath.Join(path, ".zig-cache", "fixture")
	if err := os.MkdirAll(filepath.Dir(cache), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache, []byte("build cache"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestInvestigationWorkspaceCrashHelper(t *testing.T) {
	repo := os.Getenv("OPENTUI_WORKSPACE_HELPER_REPO")
	if repo == "" {
		return
	}
	workspace, err := openInvestigationWorkspace(context.Background(), repo, os.Getenv("OPENTUI_WORKSPACE_HELPER_ROOT"), OSRunner{}, true)
	if err != nil {
		t.Fatal(err)
	}
	addTestWorkspaceWorktree(t, workspace)
	child := exec.Command("sleep", "60")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(struct {
		Path     string
		ChildPID int
	}{workspace.path, child.Process.Pid}); err != nil {
		t.Fatal(err)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func TestInvestigationWorkspaceSurvivesLiveOwnerAndRecoversSIGKILL(t *testing.T) {
	for _, recovery := range []string{"startup", "retry"} {
		t.Run(recovery, func(t *testing.T) {
			cfg := investigationGitFixture(t)
			cmd := exec.Command(os.Args[0], "-test.run=^TestInvestigationWorkspaceCrashHelper$")
			cmd.Env = append(os.Environ(), "OPENTUI_WORKSPACE_HELPER_REPO="+cfg.RepoPath, "OPENTUI_WORKSPACE_HELPER_ROOT="+cfg.WorkDir)
			cmd.Stderr = os.Stderr
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
			ready := make(chan string, 1)
			go func() {
				line, _ := bufio.NewReader(stdout).ReadString('\n')
				ready <- line
			}()
			var owner struct {
				Path     string
				ChildPID int
			}
			select {
			case line := <-ready:
				if err := json.Unmarshal([]byte(line), &owner); err != nil {
					t.Fatalf("helper readiness: %q (%v)", line, err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("helper did not acquire its execution lease")
			}
			t.Cleanup(func() { _ = syscall.Kill(owner.ChildPID, syscall.SIGKILL) })
			if err := CleanupInvestigationWorkspaces(context.Background(), cfg.RepoPath, cfg.WorkDir); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(owner.Path); err != nil {
				t.Fatalf("sweep removed live owner's workspace: %v", err)
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = cmd.Wait()
			if err := syscall.Kill(owner.ChildPID, 0); err != nil {
				t.Fatalf("lease inheritance probe exited early: %v", err)
			}
			if recovery == "startup" {
				err = CleanupInvestigationWorkspaces(context.Background(), cfg.RepoPath, cfg.WorkDir)
			} else {
				_, err = RunInvestigation(context.Background(), cfg, &worktreeExecutor{t: t})
			}
			if err != nil {
				t.Fatal(err)
			}
			assertInvestigationWorktreesCleaned(t, cfg)
		})
	}
}

func TestInvestigationWorkspaceRecoveryRespectsOwnershipAndAliases(t *testing.T) {
	cfg := investigationGitFixture(t)
	workspace := newTestWorkspace(t, cfg)
	owned := addTestWorkspaceWorktree(t, workspace)
	unrelated := filepath.Join(t.TempDir(), "user-worktree")
	fixtureGit(t, cfg.RepoPath, "worktree", "add", "--detach", unrelated, "HEAD")
	fixtureGit(t, cfg.RepoPath, "worktree", "lock", unrelated)
	t.Cleanup(func() { fixtureGit(t, cfg.RepoPath, "worktree", "remove", "--force", "--force", unrelated) })
	unknown := filepath.Join(filepath.Dir(workspace.path), "exec-unmarked")
	if err := os.Mkdir(unknown, 0o700); err != nil {
		t.Fatal(err)
	}
	partialLease := filepath.Join(filepath.Dir(workspace.path), "exec-partial.lock")
	if err := os.WriteFile(partialLease, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "repo-alias")
	if err := os.Symlink(cfg.RepoPath, alias); err != nil {
		t.Fatal(err)
	}
	rootAlias := filepath.Join(t.TempDir(), "temp-alias")
	if err := os.Symlink(cfg.WorkDir, rootAlias); err != nil {
		t.Fatal(err)
	}
	if err := workspace.lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := CleanupInvestigationWorkspaces(context.Background(), alias, rootAlias); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(owned); !os.IsNotExist(err) {
		t.Fatalf("alias did not recover owned worktree: %v", err)
	}
	if _, err := os.Stat(partialLease); !os.IsNotExist(err) {
		t.Fatalf("interrupted lease initialization survived: %v", err)
	}
	for _, path := range []string{unknown, unrelated} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("recovery touched unrelated directory %s: %v", path, err)
		}
	}
	root, _, err := investigationWorkspaceRoot(context.Background(), unrelated, rootAlias, OSRunner{})
	if err != nil || root != filepath.Dir(workspace.path) {
		t.Fatalf("linked worktree has a different repository identity: %s (%v)", root, err)
	}
}

func TestInvestigationWorkspaceRecoversIncompleteGitRegistration(t *testing.T) {
	cfg := investigationGitFixture(t)
	workspace := newTestWorkspace(t, cfg)
	registration := filepath.Join(workspace.commonDir, "worktrees", filepath.Base(workspace.worktreePath("baseline")))
	unrelated := filepath.Join(workspace.commonDir, "worktrees", "user-initializing")
	for _, dir := range []string{registration, unrelated} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "locked"), []byte("initializing"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, role := range []string{"target", "candidate"} {
		path := workspace.worktreePath(role)
		dir := filepath.Join(workspace.commonDir, "worktrees", filepath.Base(path))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "locked"), []byte("initializing"), 0o644); err != nil {
			t.Fatal(err)
		}
		var gitdir []byte
		if role == "candidate" {
			gitdir = []byte(filepath.Join(path, ".git") + "\n")
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(path, "partial-cache"), []byte("unfinished checkout"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, "gitdir"), gitdir, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_ = workspace.lease.Close()
	if err := CleanupInvestigationWorkspaces(context.Background(), cfg.RepoPath, cfg.WorkDir); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"baseline", "target", "candidate"} {
		registration := filepath.Join(workspace.commonDir, "worktrees", filepath.Base(workspace.worktreePath(role)))
		if _, err := os.Stat(registration); !os.IsNotExist(err) {
			t.Fatalf("incomplete %s registration survived: %v", role, err)
		}
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("unrelated initializing registration was removed: %v", err)
	}
	assertNoInvestigationExecutions(t, cfg.WorkDir)
}

func TestInvestigationWorkspaceRecoversExistingInvalidWorktree(t *testing.T) {
	for _, missing := range []string{"source .git", "registration HEAD"} {
		t.Run(missing, func(t *testing.T) {
			cfg := investigationGitFixture(t)
			workspace := newTestWorkspace(t, cfg)
			source := addTestWorkspaceWorktree(t, workspace)
			path := filepath.Join(source, ".git")
			if missing == "registration HEAD" {
				path = filepath.Join(workspace.commonDir, "worktrees", filepath.Base(source), "HEAD")
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(source, ".zig-cache", "fixture")); err != nil {
				t.Fatalf("fixture must retain an existing source directory and cache: %v", err)
			}
			_ = workspace.lease.Close()
			if err := CleanupInvestigationWorkspaces(context.Background(), cfg.RepoPath, cfg.WorkDir); err != nil {
				t.Fatal(err)
			}
			assertInvestigationWorktreesCleaned(t, cfg)
		})
	}
}

type interruptedWorkspaceDeletion struct {
	CmdRunner
	source string
}

func (f interruptedWorkspaceDeletion) CombinedOutput(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	if cmd.Args[0] == "rm" {
		for _, path := range []string{".git", "packages/native/build.zig"} {
			if err := os.Remove(filepath.Join(f.source, path)); err != nil {
				return nil, err
			}
		}
		return nil, errors.New("interrupted owned workspace deletion")
	}
	return f.CmdRunner.CombinedOutput(ctx, cmd)
}

func TestInvestigationWorkspaceDeletionResumesWithRemainingCache(t *testing.T) {
	cfg := investigationGitFixture(t)
	workspace := newTestWorkspace(t, cfg)
	source := addTestWorkspaceWorktree(t, workspace)
	_ = workspace.lease.Close()
	executor := interruptedWorkspaceDeletion{CmdRunner: OSRunner{}, source: source}
	if _, err := openInvestigationWorkspace(context.Background(), cfg.RepoPath, cfg.WorkDir, executor, false); err == nil || !strings.Contains(err.Error(), "interrupted owned workspace deletion") {
		t.Fatalf("interrupted cleanup error = %v", err)
	}
	for _, path := range []string{
		workspace.lease.Name(), filepath.Join(source, ".zig-cache", "fixture"),
		filepath.Join(workspace.commonDir, "worktrees", filepath.Base(source), "gitdir"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("interrupted deletion lost its marker or remaining data %s: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(source, ".git")); !os.IsNotExist(err) {
		t.Fatalf("fixture did not interrupt deletion after unlinking .git: %v", err)
	}
	if err := CleanupInvestigationWorkspaces(context.Background(), cfg.RepoPath, cfg.WorkDir); err != nil {
		t.Fatal(err)
	}
	assertInvestigationWorktreesCleaned(t, cfg)
}

type failedWorkspaceCleanup struct{ Executor }

func (f failedWorkspaceCleanup) CombinedOutput(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	if cmd.Args[0] == "rm" {
		return nil, errors.New("injected worktree cleanup failure")
	}
	return f.Executor.CombinedOutput(ctx, cmd)
}

func TestInvestigationWorkspaceCleanupFailureBlocksNewExecutions(t *testing.T) {
	cfg := investigationGitFixture(t)
	workspace := newTestWorkspace(t, cfg)
	addTestWorkspaceWorktree(t, workspace)
	_ = workspace.lease.Close()
	executor := &worktreeExecutor{t: t}
	for range 2 {
		if _, err := RunInvestigation(context.Background(), cfg, failedWorkspaceCleanup{executor}); err == nil || !strings.Contains(err.Error(), "injected worktree cleanup failure") {
			t.Fatalf("cleanup failure did not block retry: %v", err)
		}
		entries, err := os.ReadDir(filepath.Dir(workspace.path))
		if err != nil || len(entries) != 4 || len(executor.buildDirs) != 0 {
			t.Fatalf("failed recovery allocated new execution: %v, builds %v (%v)", entries, executor.buildDirs, err)
		}
	}
	if err := CleanupInvestigationWorkspaces(context.Background(), cfg.RepoPath, cfg.WorkDir); err != nil {
		t.Fatal(err)
	}
	assertInvestigationWorktreesCleaned(t, cfg)
}

func TestInvestigationWorkspaceRootLockHasBoundedWait(t *testing.T) {
	cfg := investigationGitFixture(t)
	workspace := newTestWorkspace(t, cfg)
	rootLock, err := os.OpenFile(filepath.Join(filepath.Dir(workspace.path), ".lock"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer rootLock.Close()
	if ok, err := tryWorkspaceLock(rootLock); err != nil || !ok {
		t.Fatalf("lock root: %t, %v", ok, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := CleanupInvestigationWorkspaces(ctx, cfg.RepoPath, cfg.WorkDir); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("root lock did not respect deadline: %v", err)
	}
	if _, err := os.Stat(workspace.path); err != nil {
		t.Fatal(fmt.Errorf("root lock failed to protect initializing execution: %w", err))
	}
}
