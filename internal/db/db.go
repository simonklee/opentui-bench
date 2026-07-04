package db

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

func timeNow() string {
	return time.Now().UTC().Format(time.RFC3339)
}

const schemaSQL = `
CREATE TABLE IF NOT EXISTS runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    commit_hash TEXT NOT NULL,
    commit_hash_full TEXT,
    commit_message TEXT,
    commit_date TEXT,
    branch TEXT,
    run_date TEXT NOT NULL,
    machine_id TEXT,
    notes TEXT,
    zig_optimize TEXT DEFAULT 'ReleaseFast'
);
CREATE INDEX IF NOT EXISTS idx_runs_commit ON runs(commit_hash);
CREATE INDEX IF NOT EXISTS idx_runs_date ON runs(run_date);
CREATE INDEX IF NOT EXISTS idx_runs_branch ON runs(branch);

CREATE TABLE IF NOT EXISTS results (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id INTEGER NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    category TEXT NOT NULL,
    name TEXT NOT NULL,
    min_ns INTEGER NOT NULL,
    avg_ns INTEGER NOT NULL,
    max_ns INTEGER NOT NULL,
    std_dev_ns INTEGER NOT NULL DEFAULT 0,
    p50_ns INTEGER NOT NULL DEFAULT 0,
    p95_ns INTEGER NOT NULL DEFAULT 0,
    p99_ns INTEGER NOT NULL DEFAULT 0,
    total_ns INTEGER NOT NULL,
    iterations INTEGER NOT NULL,
    sample_count INTEGER NOT NULL DEFAULT 1,
    sample_avg_variance_ns2 REAL,
    sample_data_version INTEGER NOT NULL DEFAULT 0,
    summary_version INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX IF NOT EXISTS idx_results_run ON results(run_id);
CREATE INDEX IF NOT EXISTS idx_results_name ON results(name);
CREATE INDEX IF NOT EXISTS idx_results_category ON results(category);
CREATE UNIQUE INDEX IF NOT EXISTS idx_results_run_benchmark ON results(run_id, category, name);
CREATE INDEX IF NOT EXISTS idx_results_benchmark_run ON results(category, name, run_id);

CREATE TABLE IF NOT EXISTS mem_stats (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    result_id INTEGER NOT NULL REFERENCES results(id) ON DELETE CASCADE,
    stat_name TEXT NOT NULL,
    bytes INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_mem_stats_result ON mem_stats(result_id);

CREATE TABLE IF NOT EXISTS result_samples (
    result_id INTEGER NOT NULL REFERENCES results(id) ON DELETE CASCADE,
    sample_index INTEGER NOT NULL,
    avg_ns INTEGER NOT NULL CHECK (avg_ns > 0),
    PRIMARY KEY (result_id, sample_index)
);

CREATE TABLE IF NOT EXISTS flamegraphs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id INTEGER NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    benchmark_name TEXT NOT NULL,
    folded_stacks_gz BLOB NOT NULL,
    sampling_freq INTEGER NOT NULL DEFAULT 997,
    created_at TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_flamegraphs_run_benchmark ON flamegraphs(run_id, benchmark_name);

CREATE TABLE IF NOT EXISTS artifacts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    result_id INTEGER NOT NULL REFERENCES results(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    data_blob BLOB NOT NULL,
    metadata TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL,
    UNIQUE(result_id, kind)
);
CREATE INDEX IF NOT EXISTS idx_artifacts_result_kind ON artifacts(result_id, kind);

CREATE TABLE IF NOT EXISTS jobs (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    status        TEXT NOT NULL DEFAULT 'pending',
    kind          TEXT NOT NULL DEFAULT 'benchmark',
    branch        TEXT NOT NULL,
    commit_hash   TEXT,
    repo_url      TEXT NOT NULL DEFAULT 'origin',
    samples       INTEGER NOT NULL DEFAULT 3,
    profile       TEXT NOT NULL DEFAULT 'cpu',
    notes         TEXT,
    created_at    TEXT NOT NULL,
    started_at    TEXT,
    completed_at  TEXT,
    error         TEXT,
    run_id        INTEGER REFERENCES runs(id),
    requested_by  TEXT
);
CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status);
CREATE INDEX IF NOT EXISTS idx_jobs_created ON jobs(created_at);

CREATE TABLE IF NOT EXISTS regression_cache (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id INTEGER NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    branch TEXT NOT NULL,
    window INTEGER NOT NULL,
    min_points INTEGER NOT NULL,
    baseline_offset INTEGER NOT NULL,
    generation_key TEXT NOT NULL,
    response_json TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(run_id, branch, window, min_points, baseline_offset)
);
CREATE INDEX IF NOT EXISTS idx_regression_cache_branch_run ON regression_cache(branch, run_id DESC);
CREATE INDEX IF NOT EXISTS idx_regression_cache_generation ON regression_cache(generation_key);

CREATE VIEW IF NOT EXISTS results_with_run AS
SELECT r.id AS result_id, r.category, r.name, r.min_ns, r.avg_ns, r.max_ns,
       r.std_dev_ns, r.p50_ns, r.p95_ns, r.p99_ns, r.total_ns, r.iterations,
       r.sample_count, r.sample_avg_variance_ns2, r.sample_data_version,
       r.summary_version, ru.id AS run_id, ru.commit_hash, ru.commit_hash_full,
       ru.commit_message, ru.commit_date, ru.branch, ru.run_date, ru.machine_id, ru.notes
FROM results r JOIN runs ru ON r.run_id = ru.id;
`

const (
	CurrentSchemaVersion     = 4
	CurrentSampleDataVersion = 1
	CurrentSummaryVersion    = 2
)

type DB struct {
	*sql.DB
	path string
}

func (db *DB) Path() string {
	return db.path
}

func Open(dbPath string) (*DB, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	dsn := dbPath
	if strings.Contains(dbPath, "?") {
		dsn += "&_pragma=foreign_keys(1)"
	} else {
		dsn += "?_pragma=foreign_keys(1)"
	}

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := sqlDB.Ping(); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	// Serialize all access through one connection. SQLite supports only one
	// writer at a time; multiple connections cause SQLITE_BUSY errors.
	sqlDB.SetMaxOpenConns(1)

	// WAL mode: allows concurrent reads while a write is in progress.
	// Without this, readers block writers and vice versa.
	if _, err := sqlDB.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("enable WAL mode: %w", err)
	}

	// Wait up to 5s for locks instead of failing immediately.
	// Handles transient contention from concurrent HTTP requests.
	if _, err := sqlDB.Exec("PRAGMA busy_timeout=5000"); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("set busy timeout: %w", err)
	}

	if _, err := sqlDB.Exec("PRAGMA foreign_keys = ON"); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	database := &DB{DB: sqlDB, path: dbPath}

	if err := database.migrate(); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}

	return database, nil
}

type oldFlamegraph struct {
	id            int64
	runID         int64
	benchmarkName string
	foldedStacks  string
	samplingFreq  int
	createdAt     string
}

type Run struct {
	ID             int64
	CommitHash     string
	CommitHashFull string
	CommitMessage  string
	CommitDate     string
	Branch         string
	RunDate        string
	MachineID      string
	Notes          string
	ZigOptimize    string
}

type BenchmarkKey struct {
	Category string `json:"category"`
	Name     string `json:"name"`
}

type RegressionCacheKey struct {
	RunID          int64
	Branch         string
	Window         int
	MinPoints      int
	BaselineOffset int
}

type RegressionCacheEntry struct {
	Key           RegressionCacheKey
	GenerationKey string
	ResponseJSON  string
	CreatedAt     string
	UpdatedAt     string
}

type Result struct {
	ID                   int64
	RunID                int64
	Category             string
	Name                 string
	MinNs                int64
	AvgNs                int64
	MaxNs                int64
	StdDevNs             int64
	P50Ns                int64
	P95Ns                int64
	P99Ns                int64
	TotalNs              int64
	Iterations           int64
	SampleCount          int64
	SampleAvgVarianceNs2 *float64
	SampleDataVersion    int64
	SummaryVersion       int64
	MemStats             []MemStat
	Samples              []ResultSample
}

type ResultSample struct {
	ResultID    int64 `json:"-"`
	SampleIndex int64 `json:"sample_index"`
	AvgNs       int64 `json:"avg_ns"`
}

type MemStat struct {
	ID       int64
	ResultID int64
	StatName string
	Bytes    int64
}

type Flamegraph struct {
	ID            int64
	RunID         int64
	BenchmarkName string
	FoldedStacks  string
	SVG           []byte
	SamplingFreq  int
	CreatedAt     string
}

func gzipCompress(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func gzipDecompress(data []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	return io.ReadAll(r)
}

func (db *DB) InsertRun(run *Run) (int64, error) {
	res, err := db.Exec(`
		INSERT INTO runs (commit_hash, commit_hash_full, commit_message, commit_date, branch, run_date, machine_id, notes, zig_optimize)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.CommitHash, run.CommitHashFull, run.CommitMessage, run.CommitDate,
		run.Branch, run.RunDate, run.MachineID, run.Notes, run.ZigOptimize)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (db *DB) InsertResult(result *Result) (int64, error) {
	res, err := db.Exec(`
		INSERT INTO results (run_id, category, name, min_ns, avg_ns, max_ns, std_dev_ns, p50_ns, p95_ns, p99_ns, total_ns, iterations, sample_count, sample_avg_variance_ns2, sample_data_version, summary_version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		result.RunID, result.Category, result.Name,
		result.MinNs, result.AvgNs, result.MaxNs, result.StdDevNs,
		result.P50Ns, result.P95Ns, result.P99Ns,
		result.TotalNs, result.Iterations, result.SampleCount, result.SampleAvgVarianceNs2,
		result.SampleDataVersion, normalizedSummaryVersion(result.SummaryVersion))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func normalizedSummaryVersion(version int64) int64 {
	if version == 0 {
		return 1
	}
	return version
}

// InsertRunWithResults persists the complete measurement atomically. The
// returned IDs use the structured benchmark identity and are only visible after
// run, result, memory-stat, and raw-sample insertion all succeed.
func (db *DB) InsertRunWithResults(run *Run, results []Result) (int64, map[BenchmarkKey]int64, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = tx.Rollback() }()

	runID, ids, err := insertRunWithResultsTx(tx, run, results)
	if err != nil {
		return 0, nil, err
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, err
	}
	return runID, ids, nil
}

// InsertRunWithResultsIfAbsent serializes the remote idempotency check and
// insertion in one transaction. Empty commit hashes are never deduplicated.
func (db *DB) InsertRunWithResultsIfAbsent(run *Run, results []Result) (*Run, map[BenchmarkKey]int64, bool, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, nil, false, err
	}
	defer func() { _ = tx.Rollback() }()

	if run.CommitHashFull != "" {
		existing, err := getRunByMeasurementIdentityTx(tx, run.CommitHashFull, run.MachineID, run.ZigOptimize)
		if err == nil {
			ids, err := resultIDsForRunTx(tx, existing.ID)
			if err != nil {
				return nil, nil, false, err
			}
			if err := tx.Commit(); err != nil {
				return nil, nil, false, err
			}
			return existing, ids, false, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, nil, false, err
		}
	}

	runID, ids, err := insertRunWithResultsTx(tx, run, results)
	if err != nil {
		return nil, nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, false, err
	}
	stored := *run
	stored.ID = runID
	return &stored, ids, true, nil
}

func insertRunWithResultsTx(tx *sql.Tx, run *Run, results []Result) (int64, map[BenchmarkKey]int64, error) {
	res, err := tx.Exec(`INSERT INTO runs (commit_hash, commit_hash_full, commit_message, commit_date, branch, run_date, machine_id, notes, zig_optimize)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, run.CommitHash, run.CommitHashFull, run.CommitMessage,
		run.CommitDate, run.Branch, run.RunDate, run.MachineID, run.Notes, run.ZigOptimize)
	if err != nil {
		return 0, nil, err
	}
	runID, err := res.LastInsertId()
	if err != nil {
		return 0, nil, err
	}
	ids := make(map[BenchmarkKey]int64, len(results))
	for i := range results {
		result := &results[i]
		res, err := tx.Exec(`INSERT INTO results (run_id, category, name, min_ns, avg_ns, max_ns, std_dev_ns, p50_ns, p95_ns, p99_ns, total_ns, iterations, sample_count, sample_avg_variance_ns2, sample_data_version, summary_version)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, runID, result.Category, result.Name,
			result.MinNs, result.AvgNs, result.MaxNs, result.StdDevNs, result.P50Ns, result.P95Ns,
			result.P99Ns, result.TotalNs, result.Iterations, result.SampleCount,
			result.SampleAvgVarianceNs2, result.SampleDataVersion, normalizedSummaryVersion(result.SummaryVersion))
		if err != nil {
			return 0, nil, err
		}
		resultID, err := res.LastInsertId()
		if err != nil {
			return 0, nil, err
		}
		ids[BenchmarkKey{Category: result.Category, Name: result.Name}] = resultID
		for _, stat := range result.MemStats {
			if _, err := tx.Exec(`INSERT INTO mem_stats(result_id, stat_name, bytes) VALUES (?, ?, ?)`, resultID, stat.StatName, stat.Bytes); err != nil {
				return 0, nil, err
			}
		}
		for _, sample := range result.Samples {
			if _, err := tx.Exec(`INSERT INTO result_samples(result_id, sample_index, avg_ns) VALUES (?, ?, ?)`, resultID, sample.SampleIndex, sample.AvgNs); err != nil {
				return 0, nil, err
			}
		}
	}
	return runID, ids, nil
}

func getRunByMeasurementIdentityTx(tx *sql.Tx, commitHashFull, machineID, zigOptimize string) (*Run, error) {
	var run Run
	var commitHashFullN, commitMessage, commitDate, branch, machineIDN, notes, zigOptimizeN sql.NullString
	err := tx.QueryRow(`
		SELECT id, commit_hash, commit_hash_full, commit_message, commit_date, branch, run_date, machine_id, notes, zig_optimize
		FROM runs WHERE commit_hash_full = ? AND machine_id = ? AND zig_optimize = ?
		ORDER BY julianday(run_date) DESC, id DESC LIMIT 1`, commitHashFull, machineID, zigOptimize).Scan(
		&run.ID, &run.CommitHash, &commitHashFullN, &commitMessage, &commitDate, &branch, &run.RunDate, &machineIDN, &notes, &zigOptimizeN)
	if err != nil {
		return nil, err
	}
	run.CommitHashFull = commitHashFullN.String
	run.CommitMessage = commitMessage.String
	run.CommitDate = commitDate.String
	run.Branch = branch.String
	run.MachineID = machineIDN.String
	run.Notes = notes.String
	run.ZigOptimize = zigOptimizeN.String
	return &run, nil
}

func resultIDsForRunTx(tx *sql.Tx, runID int64) (map[BenchmarkKey]int64, error) {
	rows, err := tx.Query(`SELECT id, category, name FROM results WHERE run_id = ?`, runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	ids := make(map[BenchmarkKey]int64)
	for rows.Next() {
		var id int64
		var key BenchmarkKey
		if err := rows.Scan(&id, &key.Category, &key.Name); err != nil {
			return nil, err
		}
		ids[key] = id
	}
	return ids, rows.Err()
}

func (db *DB) InsertMemStat(stat *MemStat) error {
	_, err := db.Exec(`
		INSERT INTO mem_stats (result_id, stat_name, bytes)
		VALUES (?, ?, ?)`,
		stat.ResultID, stat.StatName, stat.Bytes)
	return err
}

func (db *DB) ListRuns(limit int, branch string, since string) ([]Run, error) {
	query := `SELECT id, commit_hash, commit_hash_full, commit_message, commit_date, branch, run_date, machine_id, notes, zig_optimize FROM runs WHERE 1=1`
	args := []interface{}{}

	if branch != "" {
		query += " AND branch = ?"
		args = append(args, branch)
	}
	if since != "" {
		query += " AND run_date >= ?"
		args = append(args, since)
	}

	query += " ORDER BY run_date DESC"
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	var runs []Run
	for rows.Next() {
		var r Run
		var commitHashFull, commitMessage, commitDate, branch, machineID, notes, zigOptimize sql.NullString
		if err := rows.Scan(&r.ID, &r.CommitHash, &commitHashFull, &commitMessage, &commitDate, &branch, &r.RunDate, &machineID, &notes, &zigOptimize); err != nil {
			return nil, err
		}
		r.CommitHashFull = commitHashFull.String
		r.CommitMessage = commitMessage.String
		r.CommitDate = commitDate.String
		r.Branch = branch.String
		r.MachineID = machineID.String
		r.Notes = notes.String
		r.ZigOptimize = zigOptimize.String
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

func (db *DB) GetRun(id int64) (*Run, error) {
	var r Run
	var commitHashFull, commitMessage, commitDate, branch, machineID, notes, zigOptimize sql.NullString
	err := db.QueryRow(`
		SELECT id, commit_hash, commit_hash_full, commit_message, commit_date, branch, run_date, machine_id, notes, zig_optimize
		FROM runs WHERE id = ?`, id).Scan(
		&r.ID, &r.CommitHash, &commitHashFull, &commitMessage, &commitDate, &branch, &r.RunDate, &machineID, &notes, &zigOptimize)
	if err != nil {
		return nil, err
	}
	r.CommitHashFull = commitHashFull.String
	r.CommitMessage = commitMessage.String
	r.CommitDate = commitDate.String
	r.Branch = branch.String
	r.MachineID = machineID.String
	r.Notes = notes.String
	r.ZigOptimize = zigOptimize.String
	return &r, nil
}

func (db *DB) GetRunByCommit(commitHash string) (*Run, error) {
	var r Run
	var commitHashFull, commitMessage, commitDate, branch, machineID, notes, zigOptimize sql.NullString
	err := db.QueryRow(`
		SELECT id, commit_hash, commit_hash_full, commit_message, commit_date, branch, run_date, machine_id, notes, zig_optimize
		FROM runs WHERE commit_hash = ? OR commit_hash_full = ? ORDER BY run_date DESC LIMIT 1`, commitHash, commitHash).Scan(
		&r.ID, &r.CommitHash, &commitHashFull, &commitMessage, &commitDate, &branch, &r.RunDate, &machineID, &notes, &zigOptimize)
	if err != nil {
		return nil, err
	}
	r.CommitHashFull = commitHashFull.String
	r.CommitMessage = commitMessage.String
	r.CommitDate = commitDate.String
	r.Branch = branch.String
	r.MachineID = machineID.String
	r.Notes = notes.String
	r.ZigOptimize = zigOptimize.String
	return &r, nil
}

func (db *DB) GetLatestRun() (*Run, error) {
	var r Run
	var commitHashFull, commitMessage, commitDate, branch, machineID, notes, zigOptimize sql.NullString
	err := db.QueryRow(`
		SELECT id, commit_hash, commit_hash_full, commit_message, commit_date, branch, run_date, machine_id, notes, zig_optimize
		FROM runs ORDER BY run_date DESC LIMIT 1`).Scan(
		&r.ID, &r.CommitHash, &commitHashFull, &commitMessage, &commitDate, &branch, &r.RunDate, &machineID, &notes, &zigOptimize)
	if err != nil {
		return nil, err
	}
	r.CommitHashFull = commitHashFull.String
	r.CommitMessage = commitMessage.String
	r.CommitDate = commitDate.String
	r.Branch = branch.String
	r.MachineID = machineID.String
	r.Notes = notes.String
	r.ZigOptimize = zigOptimize.String
	return &r, nil
}

// GetLatestRunForBranch returns the most recent run for a given branch.
// An empty or "main" branch matches legacy NULL/empty values too.
func (db *DB) GetLatestRunForBranch(branch string) (*Run, error) {
	var query string
	var args []interface{}
	if branch == "main" || branch == "" {
		query = `
			SELECT id, commit_hash, commit_hash_full, commit_message, commit_date, branch, run_date, machine_id, notes, zig_optimize
			FROM runs
			WHERE branch = 'main' OR branch IS NULL OR branch = ''
			ORDER BY julianday(run_date) DESC, id DESC LIMIT 1`
	} else {
		query = `
			SELECT id, commit_hash, commit_hash_full, commit_message, commit_date, branch, run_date, machine_id, notes, zig_optimize
			FROM runs
			WHERE branch = ?
			ORDER BY julianday(run_date) DESC, id DESC LIMIT 1`
		args = append(args, branch)
	}

	var r Run
	var commitHashFull, commitMessage, commitDate, branchVal, machineID, notes, zigOptimize sql.NullString
	err := db.QueryRow(query, args...).Scan(
		&r.ID, &r.CommitHash, &commitHashFull, &commitMessage, &commitDate, &branchVal, &r.RunDate, &machineID, &notes, &zigOptimize)
	if err != nil {
		return nil, err
	}
	r.CommitHashFull = commitHashFull.String
	r.CommitMessage = commitMessage.String
	r.CommitDate = commitDate.String
	r.Branch = branchVal.String
	r.MachineID = machineID.String
	r.Notes = notes.String
	r.ZigOptimize = zigOptimize.String
	return &r, nil
}

// ListRunsForBranch returns runs for the requested branch ordered by newest first.
// "main" includes legacy NULL/empty branch values.
func (db *DB) ListRunsForBranch(branch string, limit int) ([]Run, error) {
	query := `
		SELECT id, commit_hash, commit_hash_full, commit_message, commit_date, branch, run_date, machine_id, notes, zig_optimize
		FROM runs
		WHERE `
	args := []interface{}{}

	if branch == "main" || branch == "" {
		query += `(branch = 'main' OR branch IS NULL OR branch = '')`
	} else {
		query += `branch = ?`
		args = append(args, branch)
	}

	query += ` ORDER BY julianday(run_date) DESC, id DESC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	var runs []Run
	for rows.Next() {
		var r Run
		var commitHashFull, commitMessage, commitDate, branchVal, machineID, notes, zigOptimize sql.NullString
		if err := rows.Scan(&r.ID, &r.CommitHash, &commitHashFull, &commitMessage, &commitDate, &branchVal, &r.RunDate, &machineID, &notes, &zigOptimize); err != nil {
			return nil, err
		}
		r.CommitHashFull = commitHashFull.String
		r.CommitMessage = commitMessage.String
		r.CommitDate = commitDate.String
		r.Branch = branchVal.String
		r.MachineID = machineID.String
		r.Notes = notes.String
		r.ZigOptimize = zigOptimize.String
		runs = append(runs, r)
	}

	return runs, rows.Err()
}

// GetBranchesWithRuns returns distinct branch names that have at least one run,
// ordered with "main" first, then alphabetically. Legacy NULL/empty branches
// are normalized to "main".
func (db *DB) GetBranchesWithRuns() (branches []string, err error) {
	rows, err := db.Query(`
		SELECT DISTINCT CASE
			WHEN branch IS NULL OR branch = '' THEN 'main'
			ELSE branch
		END AS normalized_branch
		FROM runs
		ORDER BY
			CASE WHEN normalized_branch = 'main' THEN 0 ELSE 1 END,
			normalized_branch`)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	for rows.Next() {
		var b string
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		branches = append(branches, b)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}

	return branches, nil
}

func (db *DB) HasCommit(commitHashFull string) (bool, error) {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM runs WHERE commit_hash_full = ?`, commitHashFull).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (db *DB) GetResultsForRun(runID int64) ([]Result, error) {
	rows, err := db.Query(`
		SELECT id, run_id, category, name, min_ns, avg_ns, max_ns, 
		       COALESCE(std_dev_ns, 0), COALESCE(p50_ns, 0), COALESCE(p95_ns, 0), COALESCE(p99_ns, 0),
		       total_ns, iterations, COALESCE(sample_count, 1), sample_avg_variance_ns2,
		       sample_data_version, summary_version
		FROM results WHERE run_id = ? ORDER BY category, name`, runID)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	var results []Result
	for rows.Next() {
		var r Result
		var variance sql.NullFloat64
		if err := rows.Scan(&r.ID, &r.RunID, &r.Category, &r.Name, &r.MinNs, &r.AvgNs, &r.MaxNs,
			&r.StdDevNs, &r.P50Ns, &r.P95Ns, &r.P99Ns,
			&r.TotalNs, &r.Iterations, &r.SampleCount, &variance, &r.SampleDataVersion, &r.SummaryVersion); err != nil {
			return nil, err
		}
		if variance.Valid {
			r.SampleAvgVarianceNs2 = &variance.Float64
		}
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range results {
		memStats, err := db.GetMemStatsForResult(results[i].ID)
		if err != nil {
			return nil, err
		}
		results[i].MemStats = memStats
		samples, err := db.GetResultSamples(results[i].ID)
		if err != nil {
			return nil, err
		}
		results[i].Samples = samples
	}

	return results, nil
}

func (db *DB) GetResult(resultID int64) (*Result, error) {
	var r Result
	var variance sql.NullFloat64
	err := db.QueryRow(`
		SELECT id, run_id, category, name, min_ns, avg_ns, max_ns,
		       COALESCE(std_dev_ns, 0), COALESCE(p50_ns, 0), COALESCE(p95_ns, 0), COALESCE(p99_ns, 0),
		       total_ns, iterations, COALESCE(sample_count, 1), sample_avg_variance_ns2,
		       sample_data_version, summary_version
		FROM results WHERE id = ?`, resultID).Scan(
		&r.ID, &r.RunID, &r.Category, &r.Name, &r.MinNs, &r.AvgNs, &r.MaxNs,
		&r.StdDevNs, &r.P50Ns, &r.P95Ns, &r.P99Ns,
		&r.TotalNs, &r.Iterations, &r.SampleCount, &variance, &r.SampleDataVersion, &r.SummaryVersion)
	if err != nil {
		return nil, err
	}
	if variance.Valid {
		r.SampleAvgVarianceNs2 = &variance.Float64
	}
	r.Samples, err = db.GetResultSamples(resultID)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (db *DB) GetResultSamples(resultID int64) ([]ResultSample, error) {
	rows, err := db.Query(`SELECT result_id, sample_index, avg_ns FROM result_samples WHERE result_id = ? ORDER BY sample_index`, resultID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	samples := []ResultSample{}
	for rows.Next() {
		var sample ResultSample
		if err := rows.Scan(&sample.ResultID, &sample.SampleIndex, &sample.AvgNs); err != nil {
			return nil, err
		}
		samples = append(samples, sample)
	}
	return samples, rows.Err()
}

type ProfiledResult struct {
	ResultID int64
	Name     string
	Category string
}

func (db *DB) ListProfiledResults(runID int64) ([]ProfiledResult, error) {
	rows, err := db.Query(`
		SELECT r.id, r.name, r.category
		FROM results r
		JOIN artifacts a ON a.result_id = r.id
		WHERE r.run_id = ? AND a.kind = 'cpu.pprof'
		ORDER BY r.category, r.name`, runID)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	var results []ProfiledResult
	for rows.Next() {
		var row ProfiledResult
		if err := rows.Scan(&row.ResultID, &row.Name, &row.Category); err != nil {
			return nil, err
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

func (db *DB) ListFlamegraphResults(runID int64) ([]ProfiledResult, error) {
	rows, err := db.Query(`
		SELECT DISTINCT r.id, r.name, r.category
		FROM results r
		LEFT JOIN artifacts a ON a.result_id = r.id AND a.kind IN ('cpu.pprof', 'cpu.flamegraph.svg')
		LEFT JOIN flamegraphs f ON f.run_id = r.run_id AND f.benchmark_name = r.name
			AND (SELECT COUNT(*) FROM results same_name WHERE same_name.run_id = r.run_id AND same_name.name = r.name) = 1
		WHERE r.run_id = ? AND (a.id IS NOT NULL OR f.id IS NOT NULL)
		ORDER BY r.category, r.name`, runID)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	var results []ProfiledResult
	for rows.Next() {
		var row ProfiledResult
		if err := rows.Scan(&row.ResultID, &row.Name, &row.Category); err != nil {
			return nil, err
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

func (db *DB) GetMemStatsForResult(resultID int64) ([]MemStat, error) {
	rows, err := db.Query(`
		SELECT id, result_id, stat_name, bytes
		FROM mem_stats WHERE result_id = ?`, resultID)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	var stats []MemStat
	for rows.Next() {
		var s MemStat
		if err := rows.Scan(&s.ID, &s.ResultID, &s.StatName, &s.Bytes); err != nil {
			return nil, err
		}
		stats = append(stats, s)
	}
	return stats, rows.Err()
}

func (db *DB) CountResultsForRun(runID int64) (int, error) {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM results WHERE run_id = ?`, runID).Scan(&count)
	return count, err
}

func (db *DB) GetTrend(resultID int64, limit int) ([]struct {
	Run    Run
	Result Result
}, error,
) {
	refResult, err := db.GetResult(resultID)
	if err != nil {
		return nil, err
	}
	refRun, err := db.GetRun(refResult.RunID)
	if err != nil {
		return nil, err
	}

	query := `
		SELECT 
			ru.id, ru.commit_hash, ru.commit_hash_full, ru.commit_message, ru.commit_date, ru.branch, ru.run_date, ru.machine_id, ru.notes, ru.zig_optimize,
			r.id, r.run_id, r.category, r.name, r.min_ns, r.avg_ns, r.max_ns, 
			COALESCE(r.std_dev_ns, 0), COALESCE(r.p50_ns, 0), COALESCE(r.p95_ns, 0), COALESCE(r.p99_ns, 0),
			r.total_ns, r.iterations, COALESCE(r.sample_count, 1)
		FROM results r
		JOIN runs ru ON r.run_id = ru.id
		WHERE r.category = ? AND r.name = ?`

	args := []interface{}{refResult.Category, refResult.Name}
	if refRun.MachineID == "" || refRun.ZigOptimize == "" {
		query += " AND ru.id = ?"
		args = append(args, refRun.ID)
	} else {
		query += " AND ru.machine_id = ? AND ru.zig_optimize = ?"
		args = append(args, refRun.MachineID, refRun.ZigOptimize)
		if refRun.Branch == "" || refRun.Branch == "main" {
			query += " AND (ru.branch = 'main' OR ru.branch IS NULL OR ru.branch = '')"
		} else {
			query += " AND (ru.id = ? OR ((ru.branch = 'main' OR ru.branch IS NULL OR ru.branch = '') AND (julianday(ru.run_date) < julianday(?) OR (julianday(ru.run_date) = julianday(?) AND ru.id <= ?))))"
			args = append(args, refRun.ID, refRun.RunDate, refRun.RunDate, refRun.ID)
		}
		query += " AND (julianday(ru.run_date) < julianday(?) OR (julianday(ru.run_date) = julianday(?) AND ru.id <= ?))"
		args = append(args, refRun.RunDate, refRun.RunDate, refRun.ID)
	}
	query += " ORDER BY julianday(ru.run_date) DESC, ru.id DESC"
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	var results []struct {
		Run    Run
		Result Result
	}

	for rows.Next() {
		var run Run
		var result Result
		var commitHashFull, commitMessage, commitDate, branch, machineID, notes, zigOptimize sql.NullString

		if err := rows.Scan(
			&run.ID, &run.CommitHash, &commitHashFull, &commitMessage, &commitDate, &branch, &run.RunDate, &machineID, &notes, &zigOptimize,
			&result.ID, &result.RunID, &result.Category, &result.Name, &result.MinNs, &result.AvgNs, &result.MaxNs,
			&result.StdDevNs, &result.P50Ns, &result.P95Ns, &result.P99Ns,
			&result.TotalNs, &result.Iterations, &result.SampleCount,
		); err != nil {
			return nil, err
		}

		run.CommitHashFull = commitHashFull.String
		run.CommitMessage = commitMessage.String
		run.CommitDate = commitDate.String
		run.Branch = branch.String
		run.MachineID = machineID.String
		run.Notes = notes.String
		run.ZigOptimize = zigOptimize.String

		results = append(results, struct {
			Run    Run
			Result Result
		}{run, result})
	}

	return results, rows.Err()
}

func (db *DB) DeleteRun(id int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM runs WHERE id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM regression_cache`); err != nil {
		return err
	}
	return tx.Commit()
}

func (db *DB) DeleteRunsBefore(date string) (int64, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.Exec(`DELETE FROM runs WHERE run_date < ?`, date)
	if err != nil {
		return 0, err
	}
	deleted, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if deleted > 0 {
		if _, err := tx.Exec(`DELETE FROM regression_cache`); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return deleted, nil
}

// RegressionDataFingerprint identifies all persisted benchmark inputs available
// at a target's causal boundary. Future runs are deliberately excluded.
func (db *DB) RegressionDataFingerprint(runID int64) (fingerprint string, err error) {
	target, err := db.GetRun(runID)
	if err != nil {
		return "", err
	}
	rows, err := db.Query(`
		SELECT ru.id, ru.commit_hash, COALESCE(ru.commit_hash_full, ''),
		       COALESCE(ru.commit_message, ''), COALESCE(ru.commit_date, ''),
		       COALESCE(ru.branch, ''), ru.run_date, COALESCE(ru.machine_id, ''),
		       COALESCE(ru.notes, ''), COALESCE(ru.zig_optimize, ''),
		       r.id, r.category, r.name, r.avg_ns, r.std_dev_ns, r.sample_count
		FROM runs ru
		LEFT JOIN results r ON r.run_id = ru.id
		WHERE julianday(ru.run_date) < julianday(?)
		   OR (julianday(ru.run_date) = julianday(?) AND ru.id <= ?)
		ORDER BY julianday(ru.run_date), ru.id, r.category, r.name, r.id`,
		target.RunDate, target.RunDate, target.ID)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	hash := sha256.New()
	for rows.Next() {
		var sourceRunID int64
		var commitHash, commitHashFull, commitMessage, commitDate, branch, runDate, machineID, notes, zigOptimize string
		var resultID, avgNs, stdDevNs, sampleCount sql.NullInt64
		var category, name sql.NullString
		if err := rows.Scan(
			&sourceRunID, &commitHash, &commitHashFull, &commitMessage, &commitDate,
			&branch, &runDate, &machineID, &notes, &zigOptimize,
			&resultID, &category, &name, &avgNs, &stdDevNs, &sampleCount,
		); err != nil {
			return "", err
		}
		_, _ = fmt.Fprintf(hash, "%d:%q:%q:%q:%q:%q:%q:%q:%q:%q:%d:%q:%q:%d:%d:%d\n",
			sourceRunID, commitHash, commitHashFull, commitMessage, commitDate, branch, runDate, machineID, notes, zigOptimize,
			resultID.Int64, category.String, name.String, avgNs.Int64, stdDevNs.Int64, sampleCount.Int64)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (db *DB) InsertFlamegraph(fg *Flamegraph) error {
	compressed, err := gzipCompress([]byte(fg.FoldedStacks))
	if err != nil {
		return fmt.Errorf("compress folded stacks: %w", err)
	}
	_, err = db.Exec(`
		INSERT INTO flamegraphs (run_id, benchmark_name, folded_stacks_gz, sampling_freq, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		fg.RunID, fg.BenchmarkName, compressed, fg.SamplingFreq, fg.CreatedAt)
	return err
}

func (db *DB) GetFlamegraph(runID int64, benchmarkName string) (*Flamegraph, error) {
	var fg Flamegraph
	var compressedStacks []byte
	err := db.QueryRow(`
		SELECT id, run_id, benchmark_name, folded_stacks_gz, sampling_freq, created_at
		FROM flamegraphs WHERE run_id = ? AND benchmark_name = ?`, runID, benchmarkName).Scan(
		&fg.ID, &fg.RunID, &fg.BenchmarkName, &compressedStacks, &fg.SamplingFreq, &fg.CreatedAt)
	if err != nil {
		return nil, err
	}

	decompressed, err := gzipDecompress(compressedStacks)
	if err != nil {
		return nil, fmt.Errorf("decompress folded stacks: %w", err)
	}
	fg.FoldedStacks = string(decompressed)
	return &fg, nil
}

func (db *DB) ListFlamegraphBenchmarks(runID int64) ([]string, error) {
	rows, err := db.Query(`SELECT benchmark_name FROM flamegraphs WHERE run_id = ? ORDER BY benchmark_name`, runID)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func (db *DB) HasFlamegraph(runID int64, benchmarkName string) (bool, error) {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM flamegraphs WHERE run_id = ? AND benchmark_name = ?`, runID, benchmarkName).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (db *DB) GetRecentRunIDs(limit int) ([]int64, error) {
	rows, err := db.Query(`SELECT id FROM runs ORDER BY run_date DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

type Artifact struct {
	ID        int64
	ResultID  int64
	Kind      string
	DataBlob  []byte
	DataSize  int64
	Metadata  string
	CreatedAt string
}

func (db *DB) InsertArtifact(a *Artifact) (int64, error) {
	res, err := db.Exec(`
		INSERT INTO artifacts (result_id, kind, data_blob, metadata, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		a.ResultID, a.Kind, a.DataBlob, a.Metadata, a.CreatedAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (db *DB) InsertArtifactIfMissing(a *Artifact) error {
	_, err := db.Exec(`
		INSERT OR IGNORE INTO artifacts (result_id, kind, data_blob, metadata, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		a.ResultID, a.Kind, a.DataBlob, a.Metadata, a.CreatedAt)
	return err
}

func (db *DB) GetArtifact(resultID int64, kind string) (*Artifact, error) {
	var a Artifact
	err := db.QueryRow(`
		SELECT id, result_id, kind, data_blob, metadata, created_at
		FROM artifacts WHERE result_id = ? AND kind = ?`, resultID, kind).Scan(
		&a.ID, &a.ResultID, &a.Kind, &a.DataBlob, &a.Metadata, &a.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (db *DB) ListArtifactsForResult(resultID int64) ([]Artifact, error) {
	rows, err := db.Query(`
		SELECT id, result_id, kind, length(data_blob), metadata, created_at
		FROM artifacts WHERE result_id = ? ORDER BY kind`, resultID)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	var artifacts []Artifact
	for rows.Next() {
		var a Artifact
		if err := rows.Scan(&a.ID, &a.ResultID, &a.Kind, &a.DataSize, &a.Metadata, &a.CreatedAt); err != nil {
			return nil, err
		}
		artifacts = append(artifacts, a)
	}
	return artifacts, rows.Err()
}

// ComparableRunsWindow fetches a window of runs comparable to the given run.
// Unknown machine or optimization metadata is isolated to the reference run.
// Returns runs in reverse chronological order (most recent first).
// The window parameter controls how many runs to return (including the reference run if found).
func (db *DB) GetComparableRunsWindow(runID int64, window int) ([]Run, error) {
	// First get the reference run to find its comparison criteria
	refRun, err := db.GetRun(runID)
	if err != nil {
		return nil, err
	}

	query := `
		SELECT id, commit_hash, commit_hash_full, commit_message, commit_date, branch, run_date, machine_id, notes, zig_optimize
		FROM runs
		WHERE `
	args := []interface{}{}

	// Treat legacy empty branch values as "main" for continuity.
	if refRun.Branch == "main" || refRun.Branch == "" {
		query += `(branch = 'main' OR branch IS NULL OR branch = '')`
	} else {
		query += `branch = ?`
		args = append(args, refRun.Branch)
	}

	if refRun.MachineID == "" || refRun.ZigOptimize == "" {
		query += ` AND id = ?`
		args = append(args, refRun.ID)
	} else {
		query += ` AND machine_id = ? AND zig_optimize = ?`
		args = append(args, refRun.MachineID, refRun.ZigOptimize)
	}
	query += `
		  AND (julianday(run_date) < julianday(?) OR (julianday(run_date) = julianday(?) AND id <= ?))
		ORDER BY julianday(run_date) DESC, id DESC
		LIMIT ?`
	args = append(args, refRun.RunDate, refRun.RunDate, refRun.ID, window)

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	var runs []Run
	for rows.Next() {
		var r Run
		var commitHashFull, commitMessage, commitDate, branch, machineID, notes, zigOptimize sql.NullString
		if err := rows.Scan(&r.ID, &r.CommitHash, &commitHashFull, &commitMessage, &commitDate, &branch, &r.RunDate, &machineID, &notes, &zigOptimize); err != nil {
			return nil, err
		}
		r.CommitHashFull = commitHashFull.String
		r.CommitMessage = commitMessage.String
		r.CommitDate = commitDate.String
		r.Branch = branch.String
		r.MachineID = machineID.String
		r.Notes = notes.String
		r.ZigOptimize = zigOptimize.String
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

// GetComparableMainRunsWindow returns compatible main history available as of
// the reference run. Feature runs are never compared with future main runs.
func (db *DB) GetComparableMainRunsWindow(runID int64, window int) ([]Run, error) {
	refRun, err := db.GetRun(runID)
	if err != nil {
		return nil, err
	}
	if refRun.MachineID == "" || refRun.ZigOptimize == "" {
		return []Run{}, nil
	}

	rows, err := db.Query(`
		SELECT id, commit_hash, commit_hash_full, commit_message, commit_date, branch, run_date, machine_id, notes, zig_optimize
		FROM runs
		WHERE (branch = 'main' OR branch IS NULL OR branch = '')
		  AND machine_id = ? AND zig_optimize = ?
		  AND (julianday(run_date) < julianday(?) OR (julianday(run_date) = julianday(?) AND id <= ?))
		ORDER BY julianday(run_date) DESC, id DESC
		LIMIT ?`, refRun.MachineID, refRun.ZigOptimize, refRun.RunDate, refRun.RunDate, refRun.ID, window)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var runs []Run
	for rows.Next() {
		var r Run
		var commitHashFull, commitMessage, commitDate, branch, machineID, notes, zigOptimize sql.NullString
		if err := rows.Scan(&r.ID, &r.CommitHash, &commitHashFull, &commitMessage, &commitDate, &branch, &r.RunDate, &machineID, &notes, &zigOptimize); err != nil {
			return nil, err
		}
		r.CommitHashFull = commitHashFull.String
		r.CommitMessage = commitMessage.String
		r.CommitDate = commitDate.String
		r.Branch = branch.String
		r.MachineID = machineID.String
		r.Notes = notes.String
		r.ZigOptimize = zigOptimize.String
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

// GetResultsForBenchmarkInRuns fetches exact benchmark results across multiple runs.
// Returns a map of runID -> Result.
func (db *DB) GetResultsForBenchmarkInRuns(key BenchmarkKey, runIDs []int64) (map[int64]Result, error) {
	if len(runIDs) == 0 {
		return make(map[int64]Result), nil
	}

	// Build placeholders for IN clause
	placeholders := make([]string, len(runIDs))
	args := make([]interface{}, len(runIDs)+2)
	args[0] = key.Category
	args[1] = key.Name
	for i, id := range runIDs {
		placeholders[i] = "?"
		args[i+2] = id
	}

	query := fmt.Sprintf(`
		SELECT id, run_id, category, name, min_ns, avg_ns, max_ns,
		       COALESCE(std_dev_ns, 0), COALESCE(p50_ns, 0), COALESCE(p95_ns, 0), COALESCE(p99_ns, 0),
		       total_ns, iterations, COALESCE(sample_count, 1)
		FROM results
		WHERE category = ? AND name = ? AND run_id IN (%s)`, strings.Join(placeholders, ","))

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	results := make(map[int64]Result)
	for rows.Next() {
		var r Result
		if err := rows.Scan(&r.ID, &r.RunID, &r.Category, &r.Name, &r.MinNs, &r.AvgNs, &r.MaxNs,
			&r.StdDevNs, &r.P50Ns, &r.P95Ns, &r.P99Ns,
			&r.TotalNs, &r.Iterations, &r.SampleCount); err != nil {
			return nil, err
		}
		results[r.RunID] = r
	}
	return results, rows.Err()
}

// Job represents a queued benchmark job.
type Job struct {
	ID          int64
	Status      string // pending, running, completed, failed, cancelled
	Kind        string // benchmark
	Branch      string
	CommitHash  string // optional: specific commit, empty = branch HEAD
	RepoURL     string // git remote name or URL
	Samples     int
	Profile     string // none, cpu
	Notes       string
	CreatedAt   string
	StartedAt   string
	CompletedAt string
	Error       string
	RunID       *int64 // links to resulting benchmark run
	RequestedBy string
}

func (db *DB) InsertJob(job *Job) (int64, error) {
	var commitHash, notes, requestedBy *string
	if job.CommitHash != "" {
		commitHash = &job.CommitHash
	}
	if job.Notes != "" {
		notes = &job.Notes
	}
	if job.RequestedBy != "" {
		requestedBy = &job.RequestedBy
	}

	res, err := db.Exec(`
		INSERT INTO jobs (status, kind, branch, commit_hash, repo_url, samples, profile, notes, created_at, requested_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.Status, job.Kind, job.Branch, commitHash, job.RepoURL,
		job.Samples, job.Profile, notes, job.CreatedAt, requestedBy)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (db *DB) GetJob(id int64) (*Job, error) {
	var j Job
	var commitHash, notes, startedAt, completedAt, jobError, requestedBy sql.NullString
	var runID sql.NullInt64

	err := db.QueryRow(`
		SELECT id, status, kind, branch, commit_hash, repo_url, samples, profile, notes,
		       created_at, started_at, completed_at, error, run_id, requested_by
		FROM jobs WHERE id = ?`, id).Scan(
		&j.ID, &j.Status, &j.Kind, &j.Branch, &commitHash, &j.RepoURL,
		&j.Samples, &j.Profile, &notes,
		&j.CreatedAt, &startedAt, &completedAt, &jobError, &runID, &requestedBy)
	if err != nil {
		return nil, err
	}
	j.CommitHash = commitHash.String
	j.Notes = notes.String
	j.StartedAt = startedAt.String
	j.CompletedAt = completedAt.String
	j.Error = jobError.String
	j.RequestedBy = requestedBy.String
	if runID.Valid {
		v := runID.Int64
		j.RunID = &v
	}
	return &j, nil
}

func (db *DB) ListJobs(limit int, status string, branch string) ([]Job, error) {
	query := `SELECT id, status, kind, branch, commit_hash, repo_url, samples, profile, notes,
	                 created_at, started_at, completed_at, error, run_id, requested_by
	          FROM jobs WHERE 1=1`
	args := []interface{}{}

	if status != "" {
		query += " AND status = ?"
		args = append(args, status)
	}
	if branch != "" {
		query += " AND branch = ?"
		args = append(args, branch)
	}

	query += " ORDER BY created_at DESC"
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	var jobs []Job
	for rows.Next() {
		var j Job
		var commitHash, notes, startedAt, completedAt, jobError, requestedBy sql.NullString
		var runID sql.NullInt64

		if err := rows.Scan(
			&j.ID, &j.Status, &j.Kind, &j.Branch, &commitHash, &j.RepoURL,
			&j.Samples, &j.Profile, &notes,
			&j.CreatedAt, &startedAt, &completedAt, &jobError, &runID, &requestedBy,
		); err != nil {
			return nil, err
		}
		j.CommitHash = commitHash.String
		j.Notes = notes.String
		j.StartedAt = startedAt.String
		j.CompletedAt = completedAt.String
		j.Error = jobError.String
		j.RequestedBy = requestedBy.String
		if runID.Valid {
			v := runID.Int64
			j.RunID = &v
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// ClaimNextPendingJob atomically finds the oldest pending job and sets it to running.
// Returns nil, nil if no pending jobs exist.
func (db *DB) ClaimNextPendingJob() (*Job, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var jobID int64
	err = tx.QueryRow(`
		SELECT id FROM jobs WHERE status = 'pending' ORDER BY created_at ASC LIMIT 1`).Scan(&jobID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	now := timeNow()
	_, err = tx.Exec(`UPDATE jobs SET status = 'running', started_at = ? WHERE id = ?`, now, jobID)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return db.GetJob(jobID)
}

// CompleteJob marks a job as completed and links the resulting run.
func (db *DB) CompleteJob(jobID int64, runID int64) error {
	now := timeNow()
	_, err := db.Exec(`
		UPDATE jobs SET status = 'completed', completed_at = ?, run_id = ?, error = NULL WHERE id = ?`,
		now, runID, jobID)
	return err
}

// FailJob marks a job as failed with an error message.
func (db *DB) FailJob(jobID int64, errMsg string) error {
	now := timeNow()
	_, err := db.Exec(`
		UPDATE jobs SET status = 'failed', completed_at = ?, error = ? WHERE id = ?`,
		now, errMsg, jobID)
	return err
}

// CancelJob cancels a pending job. Returns an error if the job is not pending.
func (db *DB) CancelJob(jobID int64) error {
	res, err := db.Exec(`UPDATE jobs SET status = 'cancelled' WHERE id = ? AND status = 'pending'`, jobID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("job %d is not pending (may already be running, completed, or cancelled)", jobID)
	}
	return nil
}

// UpdateJobCommitHash sets the resolved commit hash on a job (used by worker when resolving branch HEAD).
func (db *DB) UpdateJobCommitHash(jobID int64, commitHash string) error {
	_, err := db.Exec(`UPDATE jobs SET commit_hash = ? WHERE id = ?`, commitHash, jobID)
	return err
}

func (db *DB) GetRegressionCache(key RegressionCacheKey, generationKey string) (*RegressionCacheEntry, error) {
	var entry RegressionCacheEntry
	err := db.QueryRow(`
		SELECT run_id, branch, window, min_points, baseline_offset, generation_key, response_json, created_at, updated_at
		FROM regression_cache
		WHERE run_id = ? AND branch = ? AND window = ? AND min_points = ? AND baseline_offset = ? AND generation_key = ?`,
		key.RunID, key.Branch, key.Window, key.MinPoints, key.BaselineOffset, generationKey,
	).Scan(
		&entry.Key.RunID,
		&entry.Key.Branch,
		&entry.Key.Window,
		&entry.Key.MinPoints,
		&entry.Key.BaselineOffset,
		&entry.GenerationKey,
		&entry.ResponseJSON,
		&entry.CreatedAt,
		&entry.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return &entry, nil
}

func (db *DB) UpsertRegressionCache(entry *RegressionCacheEntry) error {
	now := timeNow()
	if entry.CreatedAt == "" {
		entry.CreatedAt = now
	}
	entry.UpdatedAt = now

	_, err := db.Exec(`
		INSERT INTO regression_cache (
			run_id, branch, window, min_points, baseline_offset,
			generation_key, response_json, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id, branch, window, min_points, baseline_offset)
		DO UPDATE SET
			generation_key = excluded.generation_key,
			response_json = excluded.response_json,
			updated_at = excluded.updated_at`,
		entry.Key.RunID,
		entry.Key.Branch,
		entry.Key.Window,
		entry.Key.MinPoints,
		entry.Key.BaselineOffset,
		entry.GenerationKey,
		entry.ResponseJSON,
		entry.CreatedAt,
		entry.UpdatedAt,
	)

	return err
}

// GetDistinctBenchmarkKeys returns all unique benchmark identities from a set of runs.
func (db *DB) GetDistinctBenchmarkKeys(runIDs []int64) ([]BenchmarkKey, error) {
	if len(runIDs) == 0 {
		return []BenchmarkKey{}, nil
	}

	placeholders := make([]string, len(runIDs))
	args := make([]interface{}, len(runIDs))
	for i, id := range runIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	query := fmt.Sprintf(`
		SELECT DISTINCT category, name FROM results
		WHERE run_id IN (%s)
		ORDER BY category, name`, strings.Join(placeholders, ","))

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	var keys []BenchmarkKey
	for rows.Next() {
		var key BenchmarkKey
		if err := rows.Scan(&key.Category, &key.Name); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}
