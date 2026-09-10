package runner

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"slices"
	"testing"

	"opentui-bench/internal/db"
	"opentui-bench/internal/profilediff"
	"opentui-bench/internal/record"
)

type recordingCmdRunner struct {
	args []string
	dir  string
}

func TestPerfUnknownFramesDoNotQualifyAsResolvedEvidence(t *testing.T) {
	prof, err := perfScriptToProfile([]byte("bench 1 [001] 1.0: cycles:\n\t0000000000001234 [unknown] ([unknown])\n\n"))
	if err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	if err := prof.Write(&data); err != nil {
		t.Fatal(err)
	}
	if hasSymbols(data.Bytes()) {
		t.Fatal("perf placeholder was counted as a resolved symbol")
	}
	diff, quality, _, err := profilediff.Diff(data.Bytes(), data.Bytes(), 10)
	if err != nil || !quality.Insufficient || quality.ResolvedFrameCount != 0 || quality.UnresolvedFrameCount != 1 || len(diff) != 0 {
		t.Fatalf("unresolved capture was qualified: diff=%v quality=%+v err=%v", diff, quality, err)
	}
}

func (r *recordingCmdRunner) CombinedOutput(_ context.Context, cmd *exec.Cmd) ([]byte, error) {
	r.args = append([]string(nil), cmd.Args...)
	r.dir = cmd.Dir
	return nil, errors.New("stop after command capture")
}

func TestCaptureCPUProfileSelectsCategoryAndName(t *testing.T) {
	runner := &recordingCmdRunner{}
	_, _, err := CaptureCPUProfile(context.Background(), runner, "/tmp/bench", "/repo/packages/native", db.BenchmarkKey{
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
	if runner.dir != "/repo/packages/native" {
		t.Fatalf("command directory = %q, want native project directory", runner.dir)
	}
}

func TestProfileSelectorRejectsSubstringCollision(t *testing.T) {
	results := []record.ParsedResult{
		{Category: "Terminal Image", Name: "Sixel 160x240 flat"},
		{Category: "Terminal Image", Name: "Sixel 160x240 flat count-only"},
	}
	if hasUniqueProfileSelector(results, db.BenchmarkKey{Category: results[0].Category, Name: results[0].Name}) {
		t.Fatal("accepted selector that also matches count-only benchmark")
	}
	if !hasUniqueProfileSelector(results, db.BenchmarkKey{Category: results[1].Category, Name: results[1].Name}) {
		t.Fatal("rejected unique count-only selector")
	}
}
