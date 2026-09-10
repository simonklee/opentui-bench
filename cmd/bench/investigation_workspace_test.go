package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"opentui-bench/internal/db"
	"opentui-bench/internal/joblease"
	"opentui-bench/internal/runner"
)

func TestWorkerOnceWithNoJobsRecoversInvestigationWorkspace(t *testing.T) {
	for _, remote := range []bool{false, true} {
		mode := "local"
		if remote {
			mode = "remote"
		}
		t.Run(mode, func(t *testing.T) {
			workDir := t.TempDir()
			t.Setenv("TMPDIR", workDir)
			t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			repo := t.TempDir()
			git := func(args ...string) string {
				t.Helper()
				out, err := runGitCommand(context.Background(), repo, args...)
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			git("init", "--quiet", "--template=", "--initial-branch=main")
			if err := os.MkdirAll(filepath.Join(repo, "packages", "native"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(repo, "packages", "native", "build.zig"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
			git("add", "--all")
			git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "--quiet", "--no-gpg-sign", "-m", "fixture")
			if err := runner.CleanupInvestigationWorkspaces(context.Background(), repo, ""); err != nil {
				t.Fatal(err)
			}
			roots, err := filepath.Glob(filepath.Join(workDir, "opentui-investigations-*"))
			if err != nil || len(roots) != 1 {
				t.Fatalf("workspace root = %v (%v)", roots, err)
			}
			marker, err := os.ReadFile(filepath.Join(roots[0], ".owner"))
			if err != nil {
				t.Fatal(err)
			}
			workspace := filepath.Join(roots[0], "exec-startup")
			if err := os.WriteFile(workspace+".lock", marker, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(workspace, 0o700); err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(workspace, "exec-startup-baseline-source")
			git("worktree", "add", "--detach", source, "HEAD")
			git("worktree", "lock", "--reason", "initializing", source)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if remote {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/api/capabilities":
						_ = json.NewEncoder(w).Encode(map[string]int{"job_lease_protocol": joblease.Protocol})
					case "/api/jobs/claim":
						w.WriteHeader(http.StatusNoContent)
					default:
						t.Errorf("unexpected worker request: %s", r.URL.Path)
					}
				}))
				defer server.Close()
				err = runWorkerRemote(ctx, &runner.RemoteRecorder{BaseURL: server.URL}, repo, time.Minute, true, "")
			} else {
				database, openErr := db.Open(filepath.Join(t.TempDir(), "bench.db"))
				if openErr != nil {
					t.Fatal(openErr)
				}
				defer database.Close()
				err = runWorkerLocal(ctx, database, repo, time.Minute, true, "")
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{workspace, workspace + ".lock"} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("idle worker left abandoned workspace %s: %v", path, err)
				}
			}
			if list := git("worktree", "list", "--porcelain"); strings.Count(list, "worktree ") != 1 {
				t.Fatalf("idle worker left abandoned registration: %s", list)
			}
		})
	}
}
