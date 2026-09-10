package runner

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const workspaceMarker = "opentui-bench investigation workspace v1\n"

type investigationWorkspace struct {
	path      string
	repoPath  string
	commonDir string
	lease     *os.File
}

// CleanupInvestigationWorkspaces recovers executions whose worker no longer holds
// its lease. Repository aliases share a root through their canonical Git directory.
func CleanupInvestigationWorkspaces(ctx context.Context, repoPath, workDir string) error {
	_, err := openInvestigationWorkspace(ctx, repoPath, workDir, OSRunner{}, false)
	return err
}

func openInvestigationWorkspace(ctx context.Context, repoPath, workDir string, executor CmdRunner, create bool) (*investigationWorkspace, error) {
	ctx, cancel := context.WithTimeout(ctx, investigationCleanupTimeout)
	defer cancel()
	root, commonDir, err := investigationWorkspaceRoot(ctx, repoPath, workDir, executor)
	if err != nil {
		return nil, err
	}
	if err := os.Mkdir(root, 0o700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Getuid()) {
		return nil, fmt.Errorf("investigation root is not a private owned directory: %s", root)
	}
	rootLock, err := os.OpenFile(filepath.Join(root, ".lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	defer rootLock.Close()
	if err := waitWorkspaceLock(ctx, rootLock); err != nil {
		return nil, fmt.Errorf("lock investigation root: %w", err)
	}
	marker := workspaceMarker + commonDir + "\n"
	if err := initializeWorkspaceRoot(root, marker); err != nil {
		return nil, err
	}
	if err := sweepInvestigationWorkspaces(ctx, root, repoPath, commonDir, marker, executor); err != nil {
		return nil, fmt.Errorf("recover investigation workspaces: %w", err)
	}
	if !create {
		return nil, nil
	}
	path := filepath.Join(root, "exec-"+rand.Text())
	lease, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_EXCL|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	// The marker is durable before the directory can contain worktrees. Go's file
	// descriptors are CLOEXEC, so subprocesses cannot keep a dead worker's lease.
	if err := waitWorkspaceLock(ctx, lease); err != nil {
		_ = lease.Close()
		_ = os.Remove(lease.Name())
		return nil, err
	}
	if _, err := lease.WriteString(marker); err != nil {
		_ = lease.Close()
		_ = os.Remove(lease.Name())
		return nil, err
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		_ = lease.Close()
		_ = os.Remove(lease.Name())
		return nil, err
	}
	return &investigationWorkspace{path: path, repoPath: repoPath, commonDir: commonDir, lease: lease}, nil
}

func investigationWorkspaceRoot(ctx context.Context, repoPath, workDir string, executor CmdRunner) (string, string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--path-format=absolute", "--git-common-dir")
	cmd.Dir = repoPath
	out, err := executor.CombinedOutput(ctx, cmd)
	if err != nil {
		return "", "", fmt.Errorf("resolve repository workspace identity: %w\n%s", err, out)
	}
	commonDir, err := filepath.EvalSymlinks(strings.TrimSpace(string(out)))
	if err != nil {
		return "", "", err
	}
	commonDir, err = filepath.Abs(commonDir)
	if err != nil {
		return "", "", err
	}
	if workDir == "" {
		workDir = os.TempDir()
	}
	workDir, err = filepath.EvalSymlinks(workDir)
	if err != nil {
		return "", "", err
	}
	workDir, err = filepath.Abs(workDir)
	if err != nil {
		return "", "", err
	}
	digest := sha256.Sum256([]byte(commonDir))
	return filepath.Join(workDir, fmt.Sprintf("opentui-investigations-%d-%x", os.Getuid(), digest)), commonDir, nil
}

func initializeWorkspaceRoot(root, marker string) error {
	path := filepath.Join(root, ".owner")
	data, err := os.ReadFile(path)
	if err == nil && string(data) == marker {
		return nil
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != ".lock" && entry.Name() != ".owner" {
			return fmt.Errorf("investigation root has no matching ownership marker: %s", root)
		}
	}
	return os.WriteFile(path, []byte(marker), 0o600)
}

func waitWorkspaceLock(ctx context.Context, file *os.File) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		locked, err := tryWorkspaceLock(file)
		if err != nil || locked {
			return err
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func tryWorkspaceLock(file *os.File) (bool, error) {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return false, nil
	}
	return err == nil, err
}

func sweepInvestigationWorkspaces(ctx context.Context, root, repoPath, commonDir, marker string, executor CmdRunner) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasPrefix(entry.Name(), "exec-") || !strings.HasSuffix(entry.Name(), ".lock") {
			continue
		}
		lease, err := os.OpenFile(filepath.Join(root, entry.Name()), os.O_RDWR|syscall.O_NOFOLLOW, 0)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		err = recoverInvestigationWorkspace(ctx, repoPath, commonDir, marker, lease, executor)
		_ = lease.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func recoverInvestigationWorkspace(ctx context.Context, repoPath, commonDir, marker string, lease *os.File, executor CmdRunner) error {
	locked, err := tryWorkspaceLock(lease)
	if err != nil || !locked {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(lease, int64(len(marker)+1)))
	if err != nil {
		return err
	}
	path := strings.TrimSuffix(lease.Name(), ".lock")
	if string(data) != marker {
		// A crash before writing the lease marker cannot have created a workspace.
		if _, err := os.Lstat(path); os.IsNotExist(err) && strings.HasPrefix(marker, string(data)) {
			return os.Remove(lease.Name())
		}
		return nil
	}
	workspace := investigationWorkspace{path: path, repoPath: repoPath, commonDir: commonDir, lease: lease}
	return workspace.remove(ctx, executor)
}

func (workspace *investigationWorkspace) worktreePath(role string) string {
	return filepath.Join(workspace.path, filepath.Base(workspace.path)+"-"+role+"-source")
}

func (workspace *investigationWorkspace) remove(ctx context.Context, executor CmdRunner) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if info, err := os.Lstat(workspace.path); err == nil && !info.IsDir() {
		return fmt.Errorf("owned workspace is not a directory: %s", workspace.path)
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, role := range []string{"baseline", "target", "candidate"} {
		if err := ctx.Err(); err != nil {
			return err
		}
		path := workspace.worktreePath(role)
		registration := filepath.Join(workspace.commonDir, "worktrees", filepath.Base(path))
		gitdir, err := os.ReadFile(filepath.Join(registration, "gitdir"))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if len(gitdir) != 0 {
			gitdirPath := strings.TrimSpace(string(gitdir))
			if !filepath.IsAbs(gitdirPath) {
				gitdirPath = filepath.Join(registration, gitdirPath)
			}
			if filepath.Clean(gitdirPath) != filepath.Join(path, ".git") {
				return fmt.Errorf("worktree registration does not belong to workspace: %s", registration)
			}
		}
		// The lease nonce owns even an incomplete registration. Git's worktree
		// removal rejects an existing source directory after its .git is deleted.
		if err := removeOwnedWorkspacePaths(ctx, executor, path, registration); err != nil {
			return err
		}
	}
	if err := removeOwnedWorkspacePaths(ctx, executor, workspace.path); err != nil {
		return err
	}
	if err := os.Remove(workspace.lease.Name()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func removeOwnedWorkspacePaths(ctx context.Context, executor CmdRunner, paths ...string) error {
	cmd := exec.CommandContext(ctx, "rm", append([]string{"-rf", "--"}, paths...)...)
	if out, err := executor.CombinedOutput(ctx, cmd); err != nil {
		return fmt.Errorf("remove owned workspace paths: %w\n%s", err, out)
	}
	return nil
}
