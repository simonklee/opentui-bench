package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"opentui-bench/internal/db"
	"opentui-bench/internal/jsbench"
	"opentui-bench/internal/runner"
)

func TestRecordJavaScriptDefaultsToCanonicalSamples(t *testing.T) {
	cmd := &cobra.Command{}
	samples := 1
	cmd.Flags().IntVar(&samples, "samples", 1, "")
	cfg := runner.RunConfig{BenchmarkKind: runner.BenchmarkJS, Samples: samples}
	applyRecordDefaults(cmd, &cfg)
	if cfg.Samples != jsbench.Samples {
		t.Fatalf("samples = %d, want %d", cfg.Samples, jsbench.Samples)
	}

	if err := cmd.Flags().Set("samples", "2"); err != nil {
		t.Fatal(err)
	}
	cfg.Samples = 2
	applyRecordDefaults(cmd, &cfg)
	if cfg.Samples != 2 {
		t.Fatalf("explicit samples = %d, want 2", cfg.Samples)
	}
}

func TestTriggerJavaScriptDefaultsToCanonicalJob(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bench.db")
	withDBPath(t, path)
	cmd := triggerCmd()
	cmd.SetArgs([]string{"--branch", "main", "--kind", "js"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	database, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	job, err := database.GetJob(1)
	if err != nil {
		t.Fatal(err)
	}
	if job.BenchmarkKind != "js" || !jsbench.MatchesJob(job.BenchmarkSuite, job.ProtocolVersion,
		job.ManifestHash, job.Samples, job.Profile) {
		t.Fatalf("job is not canonical: %+v", job)
	}
}

func TestLatestCommitDefaultsToZigAndHonorsKind(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bench.db")
	withDBPath(t, path)
	database, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.InsertRun(&db.Run{CommitHash: "zig", CommitHashFull: "zig-full", RunDate: "2026-08-03T00:00:00Z", BenchmarkKind: "zig"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.InsertRun(&db.Run{CommitHash: "js", CommitHashFull: "js-full", RunDate: "2026-08-04T00:00:00Z", BenchmarkKind: "js"})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	if got := runCommandOutput(t, latestCommitCmd()); strings.TrimSpace(got) != "zig-full" {
		t.Fatalf("default latest commit = %q, want zig-full", got)
	}
	javascript := latestCommitCmd()
	javascript.SetArgs([]string{"--kind", "js"})
	if got := runCommandOutput(t, javascript); strings.TrimSpace(got) != "js-full" {
		t.Fatalf("JavaScript latest commit = %q, want js-full", got)
	}
}

func TestShowJavaScriptPrintsIdentityAndMeasurementQuality(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bench.db")
	withDBPath(t, path)
	database, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	innerRSDs := []int64{1000, 3000, 2000}
	samples := make([]db.ResultSample, len(innerRSDs))
	for i, rsd := range innerRSDs {
		samples[i] = db.ResultSample{SampleIndex: int64(i), AvgNs: int64(90 + i*10), InnerRSDPPM: &rsd}
	}
	_, _, err = database.InsertRunWithResults(&db.Run{
		CommitHash: "identity", CommitHashFull: "identity-full", CommitMessage: "JS identity",
		Branch: "main", RunDate: "2026-08-04T00:00:00Z", MachineID: "runner-1",
		BenchmarkKind: "js", BenchmarkSuite: jsbench.Suite, ProtocolVersion: jsbench.Protocol,
		BunVersion: jsbench.BunVersion, ZigVersion: jsbench.ZigVersion, ManifestHash: jsbench.ManifestDigest,
	}, []db.Result{{
		Category: "JS Layout", Name: "leaf", MinNs: 90, AvgNs: 100, MaxNs: 110,
		P50Ns: 100, SampleCount: 3, Samples: samples,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	cmd := showCmd()
	cmd.SetArgs([]string{"--kind", "js", "identity"})
	output := runCommandOutput(t, cmd)
	wantIdentity := "Kind:     js\n" +
		"Suite:    " + jsbench.Suite + "\n" +
		"Protocol: " + strconv.FormatInt(jsbench.Protocol, 10) + "\n" +
		"Bun:      " + jsbench.BunVersion + "\n" +
		"Zig:      " + jsbench.ZigVersion + "\n" +
		"Manifest: " + jsbench.ManifestDigest + "\n" +
		"Machine:  runner-1\n"
	if !strings.Contains(output, wantIdentity) {
		t.Fatalf("JavaScript show identity:\n%s\nwant block:\n%s", output, wantIdentity)
	}
	wantQuality := "Measurement quality\nMax inner RSD: 0.30%\nProcess sample RSD:\n  JS Layout/leaf: 10.00%\n"
	if !strings.Contains(output, wantQuality) {
		t.Fatalf("JavaScript show quality:\n%s\nwant block:\n%s", output, wantQuality)
	}
}

func TestCompareJavaScriptPrintsRawDeltasWithoutClassifications(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bench.db")
	withDBPath(t, path)
	database, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	identity := db.Run{
		Branch: "main", MachineID: "runner", BenchmarkKind: "js", BenchmarkSuite: jsbench.Suite,
		ProtocolVersion: jsbench.Protocol, BunVersion: jsbench.BunVersion,
		ZigVersion: jsbench.ZigVersion, ManifestHash: jsbench.ManifestDigest,
	}
	for i, values := range []struct {
		commit string
		slow   int64
		fast   int64
	}{{"baseline", 100, 200}, {"current", 200, 100}} {
		run := identity
		run.CommitHash = values.commit
		run.CommitHashFull = values.commit + "-full"
		run.RunDate = "2026-08-0" + strconv.Itoa(i+1) + "T00:00:00Z"
		runID, err := database.InsertRun(&run)
		if err != nil {
			t.Fatal(err)
		}
		for name, p50 := range map[string]int64{"slower-case": values.slow, "faster-case": values.fast} {
			if _, err := database.InsertResult(&db.Result{RunID: runID, Category: "JS", Name: name, P50Ns: p50}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	cmd := compareCmd()
	cmd.SetArgs([]string{"--kind", "js", "baseline", "current"})
	output := captureStdout(t, cmd.Execute)
	if !strings.Contains(output, "Inference/regression classification is disabled pending qualification.") {
		t.Fatalf("JavaScript comparison omitted qualification notice: %q", output)
	}
	if !strings.Contains(output, "+100.0%") || !strings.Contains(output, "-50.0%") {
		t.Fatalf("JavaScript comparison omitted raw change: %q", output)
	}
	if strings.Contains(output, "REGRESSION") || strings.Contains(strings.ToLower(output), "improvement") || strings.Contains(output, "Summary:") {
		t.Fatalf("JavaScript comparison included a classification or count: %q", output)
	}
}

func TestCompareRejectsDifferentMachineCohorts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bench.db")
	withDBPath(t, path)
	database, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for i, machine := range []string{"machine-a", "machine-b"} {
		_, err := database.InsertRun(&db.Run{
			CommitHash: "commit-" + strconv.Itoa(i), RunDate: "2026-08-0" + strconv.Itoa(i+1) + "T00:00:00Z",
			MachineID: machine, BenchmarkKind: "zig", BenchmarkSuite: "core-default", ProtocolVersion: 1,
			ZigOptimize: "ReleaseFast",
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	cmd := compareCmd()
	cmd.SetArgs([]string{"commit-0", "commit-1"})
	err = cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "different benchmark cohorts") {
		t.Fatalf("compare error = %v, want cohort rejection", err)
	}
}

func TestBackfillDoesNotTreatJavaScriptRunAsZigRun(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "packages/core/src/zig"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "packages/core/src/zig/build.zig"), []byte("// test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init"},
		{"add", "."},
		{"-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "initial"},
	} {
		command := exec.Command("git", args...)
		command.Dir = repo
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	hashOutput, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.TrimSpace(string(hashOutput))

	database, err := db.Open(filepath.Join(t.TempDir(), "bench.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	_, err = database.InsertRun(&db.Run{CommitHash: hash[:7], CommitHashFull: hash, RunDate: "2026-08-04T00:00:00Z", BenchmarkKind: "js"})
	if err != nil {
		t.Fatal(err)
	}

	output := captureStdout(t, func() error {
		return runBackfill(context.Background(), database, 1, "HEAD", true, runner.RunConfig{
			RepoPath: repo, BenchmarkKind: runner.BenchmarkZig,
		})
	})
	if !strings.Contains(output, "Found 1 unrecorded commit") {
		t.Fatalf("backfill output = %q, want unrecorded Zig commit", output)
	}
}

func TestWorkerStatusContextSurvivesShutdownAndIsBounded(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	cancelParent()
	ctx, cancel := workerStatusContext(parent)
	defer cancel()
	if err := ctx.Err(); err != nil {
		t.Fatalf("status context inherited shutdown cancellation: %v", err)
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("status context has no deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > workerStatusTimeout {
		t.Fatalf("status context remaining duration = %s", remaining)
	}
	if repositoryRestoreTimeout+workerStatusTimeout >= 30*time.Second {
		t.Fatalf("restore and status budgets total %s, want less than systemd's 30s stop timeout", repositoryRestoreTimeout+workerStatusTimeout)
	}
}

func TestCompleteLocalJobSurvivesCancellationAfterRunPersistence(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "bench.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	jobID, err := database.InsertJob(&db.Job{
		Status: "pending", Kind: "benchmark", Branch: "main", CommitHash: "resolved",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err := database.ClaimNextPendingJob("")
	if err != nil || job == nil || job.ID != jobID {
		t.Fatalf("claim job: job=%+v err=%v", job, err)
	}
	runID, err := database.InsertRun(&db.Run{
		CommitHash: "resolved", CommitHashFull: "resolved", RunDate: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := completeLocalJob(ctx, database, job, runID); err != nil {
		t.Fatalf("complete persisted run after cancellation: %v", err)
	}
	stored, err := database.GetJob(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "completed" || stored.RunID == nil || *stored.RunID != runID {
		t.Fatalf("job not completed: %+v", stored)
	}
}

func TestUpdateRemoteJobAfterExecutionHandlesShutdownRace(t *testing.T) {
	tests := []struct {
		name          string
		runID         int64
		jobErr        error
		wantStatus    string
		wantCompleted bool
	}{
		{name: "persisted run", runID: 42, jobErr: context.Canceled, wantStatus: "completed", wantCompleted: true},
		{name: "no persisted run", jobErr: context.Canceled, wantStatus: "pending"},
		{name: "artifact failure", runID: 42, jobErr: errors.New("artifact rejected"), wantStatus: "failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			var update map[string]interface{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
					t.Error(err)
				}
				_, _ = w.Write([]byte(`{}`))
			}))
			defer server.Close()

			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			completed, err := updateRemoteJobAfterExecution(ctx, &runner.RemoteRecorder{BaseURL: server.URL},
				&runner.JobClaimResponse{ID: 7, ClaimToken: "claim"}, test.runID, test.jobErr)
			if err != nil {
				t.Fatal(err)
			}
			if requests != 1 {
				t.Fatalf("status requests = %d, want 1", requests)
			}
			if update["status"] != test.wantStatus || completed != test.wantCompleted {
				t.Fatalf("update/completed = %+v/%v, want status %q completed %v", update, completed, test.wantStatus, test.wantCompleted)
			}
			if test.wantCompleted && update["run_id"] != float64(test.runID) {
				t.Fatalf("completed run_id = %v, want %d", update["run_id"], test.runID)
			}
		})
	}
}

func TestUpdateRemoteJobAfterExecutionDoesNotMutateLostClaim(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()

	completed, err := updateRemoteJobAfterExecution(context.Background(), &runner.RemoteRecorder{BaseURL: server.URL},
		&runner.JobClaimResponse{ID: 7, ClaimToken: "stale"}, 0, runner.ErrJobClaimLost)
	if err != nil || completed || requests != 0 {
		t.Fatalf("completed/error/requests = %v/%v/%d, want false/nil/0", completed, err, requests)
	}
}

func TestRestoreRepositorySurvivesCancellationAndCleansCheckout(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", repo}, args...)...)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	git("init")
	git("config", "user.name", "Test")
	git("config", "user.email", "test@example.com")
	file := filepath.Join(repo, "tracked.txt")
	if err := os.WriteFile(file, []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "tracked.txt")
	git("commit", "-m", "first")
	first := git("rev-parse", "HEAD")
	if err := os.WriteFile(file, []byte("second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("commit", "-am", "second")
	second := git("rev-parse", "HEAD")
	git("checkout", first)
	if err := os.WriteFile(file, []byte("benchmark changes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := restoreRepository(ctx, repo, second); err != nil {
		t.Fatal(err)
	}
	if head := git("rev-parse", "HEAD"); head != second {
		t.Fatalf("restored HEAD = %s, want %s", head, second)
	}
	if status := git("status", "--porcelain"); status != "" {
		t.Fatalf("restored repository is dirty: %q", status)
	}
}

func TestRunGitCommandPreservesCancellationWithExitError(t *testing.T) {
	binDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "started")
	script := filepath.Join(binDir, "git")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf started > \"$MARKER\"\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MARKER", marker)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repo := t.TempDir()
	done := make(chan error, 1)
	go func() {
		_, err := runGitCommand(ctx, repo, "status")
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("git subprocess did not start")
	}
	cancel()
	select {
	case err := <-done:
		var exitError *exec.ExitError
		if !errors.Is(err, context.Canceled) || !errors.As(err, &exitError) {
			t.Fatalf("git error = %v, want context.Canceled joined with ExitError", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled git subprocess did not exit")
	}
}

func withDBPath(t *testing.T, path string) {
	t.Helper()
	previous := dbPath
	dbPath = path
	t.Cleanup(func() { dbPath = previous })
}

func runCommandOutput(t *testing.T, cmd *cobra.Command) string {
	t.Helper()
	return captureStdout(t, cmd.Execute)
}

func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()
	previous := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	err = fn()
	_ = writer.Close()
	os.Stdout = previous
	output, readErr := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil {
		t.Fatal(err)
	}
	if readErr != nil {
		t.Fatal(readErr)
	}
	return string(output)
}
