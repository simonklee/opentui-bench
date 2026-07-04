package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"opentui-bench/internal/calibration"
	"opentui-bench/internal/db"
)

func calibrateCmd() *cobra.Command {
	opts := calibration.DefaultOptions()
	var output string
	cmd := &cobra.Command{
		Use: "calibrate", Short: "Run the frozen chronological detector calibration replay",
		Long: "Open the database in SQLite read-only mode, replay the versioned detector without future leakage, print a human report, and optionally write the machine-readable JSON report. WAL reads may touch ephemeral -shm coordination metadata.",
		RunE: func(cmd *cobra.Command, args []string) error {
			database, err := db.OpenReadOnly(dbPath)
			if err != nil {
				return err
			}
			defer func() { _ = database.Close() }()
			report, err := calibration.Run(database, opts)
			if err != nil {
				return err
			}
			calibration.WriteText(cmd.OutOrStdout(), report)
			if output != "" {
				if err := writeCalibrationJSON(dbPath, output, report); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "\nWrote calibration JSON: %s\n", output)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.Branch, "branch", "main", "branch targets to replay (feature branches use causal compatible main history)")
	cmd.Flags().Int64Var(&opts.Seed, "seed", calibration.DefaultSeed, "deterministic residual-bootstrap injection seed")
	cmd.Flags().StringVarP(&output, "output", "o", "", "new path for machine-readable JSON report (must not exist)")
	return cmd
}

func validateCalibrationOutputPath(databasePath, outputPath string) error {
	linkInfo, err := os.Lstat(outputPath)
	if err == nil && linkInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("calibration output must not be a symlink")
	}
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("inspect calibration output: %w", err)
	}
	outputCanonical, err := canonicalPath(outputPath)
	if err != nil {
		return fmt.Errorf("resolve calibration output: %w", err)
	}
	outputInfo, outputStatErr := os.Stat(outputPath)
	if outputStatErr != nil && !os.IsNotExist(outputStatErr) {
		return fmt.Errorf("stat calibration output: %w", outputStatErr)
	}

	protectedPaths, err := protectedDatabasePaths(databasePath)
	if err != nil {
		return err
	}
	for _, protectedPath := range protectedPaths {
		protectedCanonical, err := canonicalPath(protectedPath)
		if err != nil {
			return fmt.Errorf("resolve protected database path: %w", err)
		}
		if outputCanonical == protectedCanonical {
			return fmt.Errorf("calibration output must not overwrite database file %s", protectedPath)
		}
		if outputStatErr == nil {
			protectedInfo, err := os.Stat(protectedPath)
			if err == nil && os.SameFile(outputInfo, protectedInfo) {
				return fmt.Errorf("calibration output aliases database file %s", protectedPath)
			}
			if err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("stat protected database path: %w", err)
			}
		}
	}
	return nil
}

func writeCalibrationJSON(databasePath, outputPath string, report *calibration.Report) error {
	if err := validateCalibrationOutputPath(databasePath, outputPath); err != nil {
		return err
	}
	file, err := os.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create calibration output (path must not exist): %w", err)
	}
	removeOutput := true
	defer func() {
		_ = file.Close()
		if removeOutput {
			_ = os.Remove(outputPath)
		}
	}()

	outputInfo, err := file.Stat()
	if err != nil {
		return err
	}
	protectedPaths, err := protectedDatabasePaths(databasePath)
	if err != nil {
		return err
	}
	for _, protectedPath := range protectedPaths {
		protectedInfo, err := os.Stat(protectedPath)
		if err == nil && os.SameFile(outputInfo, protectedInfo) {
			return fmt.Errorf("opened calibration output aliases database file %s", protectedPath)
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := calibration.WriteJSON(file, report); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	removeOutput = false
	return nil
}

func protectedDatabasePaths(databasePath string) ([]string, error) {
	canonicalDatabase, err := canonicalPath(databasePath)
	if err != nil {
		return nil, fmt.Errorf("resolve database path: %w", err)
	}
	paths := []string{canonicalDatabase}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		paths = append(paths, canonicalDatabase+suffix)
	}
	return paths, nil
}

func canonicalPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(absolute)), nil
}
