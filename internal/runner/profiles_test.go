package runner

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"testing"

	"opentui-bench/internal/db"
)

type recordingCmdRunner struct {
	args []string
}

func (r *recordingCmdRunner) CombinedOutput(_ context.Context, cmd *exec.Cmd) ([]byte, error) {
	r.args = append([]string(nil), cmd.Args...)
	return nil, errors.New("stop after command capture")
}

func TestCaptureCPUProfileSelectsCategoryAndName(t *testing.T) {
	runner := &recordingCmdRunner{}
	_, _, err := CaptureCPUProfile(context.Background(), runner, "/tmp/bench", db.BenchmarkKey{
		Category: "buffer",
		Name:     "draw/box",
	}, 997)
	if err == nil {
		t.Fatal("expected command capture error")
	}

	wantSuffix := []string{"/tmp/bench", "--filter", "buffer", "--bench", "draw/box", "--json"}
	if len(runner.args) < len(wantSuffix) || !slices.Equal(runner.args[len(runner.args)-len(wantSuffix):], wantSuffix) {
		t.Fatalf("command suffix = %q, want %q", runner.args, wantSuffix)
	}
}
