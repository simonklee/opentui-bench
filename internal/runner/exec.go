package runner

import (
	"context"
	"os/exec"
)

// CmdRunner abstracts executing commands to allow for testing.
type CmdRunner interface {
	CombinedOutput(ctx context.Context, cmd *exec.Cmd) ([]byte, error)
}

// OSRunner implements CmdRunner using the real os/exec package.
type OSRunner struct{}

// CombinedOutput runs the command and returns combined stdout and stderr.
func (OSRunner) CombinedOutput(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	return cmd.CombinedOutput()
}
