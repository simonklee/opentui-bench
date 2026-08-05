package runner

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"syscall"
)

// CmdRunner abstracts executing commands to allow for testing.
type CmdRunner interface {
	CombinedOutput(ctx context.Context, cmd *exec.Cmd) ([]byte, error)
}

// Executor also captures protocol stdout and diagnostics separately.
type Executor interface {
	CmdRunner
	Output(ctx context.Context, cmd *exec.Cmd) (stdout, stderr []byte, err error)
}

// OSRunner implements CmdRunner using the real os/exec package.
type OSRunner struct{}

// CombinedOutput runs the command and returns combined stdout and stderr.
func (OSRunner) CombinedOutput(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	output, err := cmd.CombinedOutput()
	var exitError *exec.ExitError
	if err != nil && errors.As(err, &exitError) && ctx.Err() != nil {
		err = errors.Join(err, ctx.Err())
	}
	return output, err
}

// Output runs a command in its own process group. Cancelling ctx kills the
// entire group so benchmark descendants cannot survive a process timeout.
func (OSRunner) Output(ctx context.Context, cmd *exec.Cmd) ([]byte, []byte, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return stdout.Bytes(), stderr.Bytes(), err
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
		return stdout.Bytes(), stderr.Bytes(), ctx.Err()
	}
}
