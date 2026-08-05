package runner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestOSRunnerCombinedOutputPreservesCancellationWithExitError(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", "printf started > \"$MARKER\"; exec sleep 60")
	cmd.Env = append(os.Environ(), "MARKER="+marker)
	done := make(chan error, 1)
	go func() {
		_, err := (OSRunner{}).CombinedOutput(ctx, cmd)
		done <- err
	}()
	waitForFile(t, marker)
	cancel()
	select {
	case err := <-done:
		var exitError *exec.ExitError
		if !errors.Is(err, context.Canceled) || !errors.As(err, &exitError) {
			t.Fatalf("command error = %v, want context.Canceled joined with ExitError", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled subprocess did not exit")
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("subprocess did not start")
}
