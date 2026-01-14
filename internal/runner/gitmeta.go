package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"opentui-bench/internal/record"
)

// ReadGitMeta gathers git metadata and hostname for the run.
func ReadGitMeta(ctx context.Context, repoPath string, r CmdRunner) (record.RunMetadata, error) {
	var meta record.RunMetadata
	var err error

	// Helper to run git command in repo dir
	runGit := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = repoPath
		out, err := r.CombinedOutput(ctx, cmd)
		if err != nil {
			return "", fmt.Errorf("git %v: %w (%s)", args, err, strings.TrimSpace(string(out)))
		}
		return strings.TrimSpace(string(out)), nil
	}

	meta.CommitHash, err = runGit("rev-parse", "--short", "HEAD")
	if err != nil {
		return meta, err
	}

	meta.CommitHashFull, err = runGit("rev-parse", "HEAD")
	if err != nil {
		return meta, err
	}

	meta.CommitMessage, err = runGit("log", "-1", "--format=%s")
	if err != nil {
		return meta, err
	}

	meta.CommitDate, err = runGit("log", "-1", "--format=%cI")
	if err != nil {
		return meta, err
	}

	meta.Branch, err = runGit("branch", "--show-current")
	if err != nil {
		return meta, err
	}

	meta.MachineID, err = os.Hostname()
	if err != nil {
		// Fallback if hostname fails
		meta.MachineID = "unknown"
	}

	return meta, nil
}
