package main

import (
	"os"
	"path/filepath"
	"testing"

	"opentui-bench/internal/calibration"
)

func TestValidateCalibrationOutputRejectsDatabaseAliases(t *testing.T) {
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "bench.db")
	if err := os.WriteFile(databasePath, []byte("database"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, outputPath := range []string{databasePath, databasePath + "-wal", databasePath + "-shm", databasePath + "-journal"} {
		if err := validateCalibrationOutputPath(databasePath, outputPath); err == nil {
			t.Fatalf("expected protected output %s to be rejected", outputPath)
		}
	}

	hardlink := filepath.Join(dir, "hardlink.json")
	if err := os.Link(databasePath, hardlink); err != nil {
		t.Fatal(err)
	}
	if err := validateCalibrationOutputPath(databasePath, hardlink); err == nil {
		t.Fatal("expected database hardlink to be rejected")
	}

	symlink := filepath.Join(dir, "symlink.json")
	if err := os.Symlink(databasePath, symlink); err != nil {
		t.Fatal(err)
	}
	if err := validateCalibrationOutputPath(databasePath, symlink); err == nil {
		t.Fatal("expected database symlink to be rejected")
	}

	danglingSidecar := filepath.Join(dir, "dangling.json")
	if err := os.Symlink(databasePath+"-journal", danglingSidecar); err != nil {
		t.Fatal(err)
	}
	if err := validateCalibrationOutputPath(databasePath, danglingSidecar); err == nil {
		t.Fatal("expected dangling sidecar symlink to be rejected")
	}

	if err := validateCalibrationOutputPath(databasePath, filepath.Join(dir, "report.json")); err != nil {
		t.Fatalf("safe output rejected: %v", err)
	}

	databaseAlias := filepath.Join(dir, "database-alias.db")
	if err := os.Symlink(databasePath, databaseAlias); err != nil {
		t.Fatal(err)
	}
	if err := validateCalibrationOutputPath(databaseAlias, databasePath+"-wal"); err == nil {
		t.Fatal("expected canonical database sidecar to be rejected")
	}
}

func TestWriteCalibrationJSONCreatesNewFileOnly(t *testing.T) {
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "bench.db")
	if err := os.WriteFile(databasePath, []byte("database"), 0o600); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(dir, "report.json")
	report := &calibration.Report{ReportVersion: calibration.ReportVersion}
	if err := writeCalibrationJSON(databasePath, outputPath, report); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeCalibrationJSON(databasePath, outputPath, report); err == nil {
		t.Fatal("expected existing output to be rejected")
	}
	after, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("existing output was modified")
	}
}
