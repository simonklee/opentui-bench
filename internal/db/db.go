package db

import (
	"bytes"
	"compress/gzip"
	"context"
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
	"unicode/utf8"

	_ "modernc.org/sqlite"

	"opentui-bench/internal/joblease"
	"opentui-bench/internal/jsbench"
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
    zig_optimize TEXT DEFAULT 'ReleaseFast',
    benchmark_kind TEXT NOT NULL DEFAULT 'zig',
    benchmark_suite TEXT NOT NULL DEFAULT 'core-default',
    protocol_version INTEGER NOT NULL DEFAULT 1,
    bun_version TEXT NOT NULL DEFAULT '',
    js_runtime TEXT NOT NULL DEFAULT '',
    runtime_version TEXT NOT NULL DEFAULT '',
    zig_version TEXT NOT NULL DEFAULT '',
    manifest_hash TEXT NOT NULL DEFAULT '',
    manifest_json TEXT NOT NULL DEFAULT '',
    idempotency_key TEXT
);
CREATE INDEX IF NOT EXISTS idx_runs_commit ON runs(commit_hash);
CREATE INDEX IF NOT EXISTS idx_runs_date ON runs(run_date);
CREATE INDEX IF NOT EXISTS idx_runs_branch ON runs(branch);
CREATE UNIQUE INDEX IF NOT EXISTS idx_runs_idempotency_key ON runs(idempotency_key)
    WHERE idempotency_key IS NOT NULL AND idempotency_key <> '';

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
    inner_rsd_ppm INTEGER CHECK (inner_rsd_ppm >= 0),
    PRIMARY KEY (result_id, sample_index)
);

CREATE TABLE IF NOT EXISTS result_sample_batches (
    result_id INTEGER NOT NULL,
    sample_index INTEGER NOT NULL,
    batch_index INTEGER NOT NULL,
    elapsed_ns INTEGER NOT NULL CHECK (elapsed_ns > 0),
    iterations INTEGER NOT NULL CHECK (iterations > 0),
    PRIMARY KEY (result_id, sample_index, batch_index),
    FOREIGN KEY (result_id, sample_index)
        REFERENCES result_samples(result_id, sample_index) ON DELETE CASCADE
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
    requested_by  TEXT,
    claim_token   TEXT, -- SHA-256 digest; the raw bearer token is held only by its claimant
    legacy_tokenless INTEGER NOT NULL DEFAULT 0,
    benchmark_kind TEXT NOT NULL DEFAULT 'zig',
    benchmark_suite TEXT NOT NULL DEFAULT 'core-default',
    protocol_version INTEGER NOT NULL DEFAULT 1,
    manifest_hash TEXT NOT NULL DEFAULT '',
    js_runtime TEXT NOT NULL DEFAULT '',
    runtime_version TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status);
CREATE INDEX IF NOT EXISTS idx_jobs_created ON jobs(created_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_jobs_claim_token ON jobs(claim_token) WHERE claim_token IS NOT NULL;

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
       ru.commit_message, ru.commit_date, ru.branch, ru.run_date, ru.machine_id, ru.notes,
       ru.zig_optimize, ru.benchmark_kind, ru.benchmark_suite, ru.protocol_version,
       ru.bun_version, ru.js_runtime, ru.runtime_version, ru.zig_version, ru.manifest_hash, ru.manifest_json
FROM results r JOIN runs ru ON r.run_id = ru.id;
`

const (
	CurrentSchemaVersion     = 8
	CurrentSampleDataVersion = 1
	CurrentSummaryVersion    = 2
	DefaultProfileRunsMax    = 50
	DefaultProfileBytesMax   = int64(128 << 20)
)

type DB struct {
	*sql.DB
	path string
}

func (db *DB) Path() string {
	return db.path
}

// CompactBackup writes a consistent snapshot without copying free pages. A
// separate read connection keeps normal WAL-mode API traffic responsive.
// SQLite requires that destination does not already exist.
func (db *DB) CompactBackup(ctx context.Context, destination string) error {
	if _, err := os.Stat(destination); err == nil {
		return fmt.Errorf("compact backup destination already exists: %s", destination)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat compact backup destination: %w", err)
	}
	source, err := sql.Open("sqlite", "file:"+filepath.ToSlash(db.path)+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return fmt.Errorf("open compact backup source: %w", err)
	}
	defer func() { _ = source.Close() }()
	source.SetMaxOpenConns(1)
	if _, err := source.ExecContext(ctx, `VACUUM INTO ?`, destination); err != nil {
		removeErr := os.Remove(destination)
		if errors.Is(removeErr, os.ErrNotExist) {
			removeErr = nil
		}
		return errors.Join(fmt.Errorf("write compact backup: %w", err), removeErr)
	}
	return nil
}

type StorageStats struct {
	AllocatedBytes int64
	LiveBytes      int64
	FreeBytes      int64
	PageSize       int64
	PageCount      int64
	FreePageCount  int64
}

func (db *DB) StorageStats() (StorageStats, error) {
	var stats StorageStats
	if err := db.QueryRow(`PRAGMA page_size`).Scan(&stats.PageSize); err != nil {
		return StorageStats{}, fmt.Errorf("read SQLite page size: %w", err)
	}
	if err := db.QueryRow(`PRAGMA page_count`).Scan(&stats.PageCount); err != nil {
		return StorageStats{}, fmt.Errorf("read SQLite page count: %w", err)
	}
	if err := db.QueryRow(`PRAGMA freelist_count`).Scan(&stats.FreePageCount); err != nil {
		return StorageStats{}, fmt.Errorf("read SQLite free page count: %w", err)
	}
	if stats.PageSize <= 0 || stats.PageCount < 0 || stats.FreePageCount < 0 || stats.FreePageCount > stats.PageCount {
		return StorageStats{}, fmt.Errorf("invalid SQLite storage stats: page_size=%d page_count=%d free_page_count=%d",
			stats.PageSize, stats.PageCount, stats.FreePageCount)
	}
	stats.AllocatedBytes = stats.PageSize * stats.PageCount
	stats.FreeBytes = stats.PageSize * stats.FreePageCount
	stats.LiveBytes = stats.AllocatedBytes - stats.FreeBytes
	return stats, nil
}

// Vacuum rewrites the database to return free pages to the filesystem. Profile
// pruning does not need this for steady-state bounds because SQLite reuses free
// pages, but an explicit vacuum shrinks an already oversized database.
func (db *DB) Vacuum() error {
	if _, err := db.Exec(`VACUUM`); err != nil {
		return fmt.Errorf("vacuum database: %w", err)
	}
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return fmt.Errorf("truncate WAL after vacuum: %w", err)
	}
	return nil
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

// OpenReadOnly prevents persisted database and WAL writes. SQLite may still
// touch ephemeral -shm lock-coordination metadata while reading a WAL database.
func OpenReadOnly(dbPath string) (*DB, error) {
	dsn := "file:" + filepath.ToSlash(dbPath) + "?mode=ro&_pragma=foreign_keys(1)&_pragma=query_only(1)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open read-only database: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := sqlDB.Ping(); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping read-only database: %w", err)
	}
	var version int
	if err := sqlDB.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	if version != CurrentSchemaVersion {
		_ = sqlDB.Close()
		if version < CurrentSchemaVersion {
			return nil, fmt.Errorf("database schema version %d is older than required version %d; open it writable to migrate", version, CurrentSchemaVersion)
		}
		return nil, fmt.Errorf("database schema version %d is newer than supported version %d", version, CurrentSchemaVersion)
	}
	return &DB{DB: sqlDB, path: dbPath}, nil
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
	ID               int64
	CommitHash       string
	CommitHashFull   string
	CommitMessage    string
	CommitDate       string
	Branch           string
	RunDate          string
	MachineID        string
	Notes            string
	ZigOptimize      string
	BenchmarkKind    string
	BenchmarkSuite   string
	ProtocolVersion  int64
	BunVersion       string
	JSRuntime        string
	RuntimeVersion   string
	ZigVersion       string
	ManifestHash     string
	ManifestJSON     string
	LegacyJSIdentity bool // request-only marker for deployed schema-1 Bun retries
}

// RunFilter selects one benchmark cohort. Empty fields are unconstrained.
type RunFilter struct {
	BenchmarkKind   string
	BenchmarkSuite  string
	ProtocolVersion int64
	BunVersion      string
	JSRuntime       string
	RuntimeVersion  string
	ZigVersion      string
	ManifestHash    string
	MachineID       string
	ZigOptimize     string
}

func (filter RunFilter) Matches(run *Run) bool {
	return (filter.BenchmarkKind == "" || run.BenchmarkKind == filter.BenchmarkKind) &&
		(filter.BenchmarkSuite == "" || run.BenchmarkSuite == filter.BenchmarkSuite) &&
		(filter.ProtocolVersion == 0 || run.ProtocolVersion == filter.ProtocolVersion) &&
		(filter.BunVersion == "" || run.BunVersion == filter.BunVersion) &&
		(filter.JSRuntime == "" || run.JSRuntime == filter.JSRuntime) &&
		(filter.RuntimeVersion == "" || run.RuntimeVersion == filter.RuntimeVersion) &&
		(filter.ZigVersion == "" || run.ZigVersion == filter.ZigVersion) &&
		(filter.ManifestHash == "" || run.ManifestHash == filter.ManifestHash) &&
		(filter.MachineID == "" || run.MachineID == filter.MachineID) &&
		(filter.ZigOptimize == "" || run.ZigOptimize == filter.ZigOptimize)
}

func appendRunFilter(query string, args []interface{}, alias string, filter RunFilter) (string, []interface{}) {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	fields := []struct {
		name  string
		value string
	}{
		{"benchmark_kind", filter.BenchmarkKind},
		{"benchmark_suite", filter.BenchmarkSuite},
		{"bun_version", filter.BunVersion},
		{"js_runtime", filter.JSRuntime},
		{"runtime_version", filter.RuntimeVersion},
		{"zig_version", filter.ZigVersion},
		{"manifest_hash", filter.ManifestHash},
		{"machine_id", filter.MachineID},
		{"zig_optimize", filter.ZigOptimize},
	}
	for _, field := range fields {
		if field.value != "" {
			query += " AND " + prefix + field.name + " = ?"
			args = append(args, field.value)
		}
	}
	if filter.ProtocolVersion > 0 {
		query += " AND " + prefix + "protocol_version = ?"
		args = append(args, filter.ProtocolVersion)
	}
	return query, args
}

func runCohortFilter(run *Run) RunFilter {
	filter := RunFilter{
		BenchmarkKind: run.BenchmarkKind, BenchmarkSuite: run.BenchmarkSuite,
		ProtocolVersion: run.ProtocolVersion,
		JSRuntime:       run.JSRuntime, RuntimeVersion: run.RuntimeVersion,
		ZigVersion: run.ZigVersion, ManifestHash: run.ManifestHash, MachineID: run.MachineID,
		ZigOptimize: relevantOptimize(run),
	}
	if run.BenchmarkKind == jsbench.Kind && run.JSRuntime == "" {
		filter.BunVersion = run.BunVersion
	}
	return filter
}

// SameRunCohort reports whether two runs have the same measurement identity.
// Zig optimization is relevant only to Zig runs; the remaining identity fields
// include the machine and pinned runtime/manifest values used by JavaScript.
func SameRunCohort(a, b *Run) bool {
	if a == nil || b == nil {
		return false
	}
	return runCohortFilter(a) == runCohortFilter(b)
}

type scanner interface {
	Scan(dest ...any) error
}

func scanRun(s scanner, run *Run) error {
	var commitHashFull, commitMessage, commitDate, branch, machineID, notes, zigOptimize sql.NullString
	if err := s.Scan(&run.ID, &run.CommitHash, &commitHashFull, &commitMessage, &commitDate, &branch,
		&run.RunDate, &machineID, &notes, &zigOptimize, &run.BenchmarkKind, &run.BenchmarkSuite,
		&run.ProtocolVersion, &run.BunVersion, &run.JSRuntime, &run.RuntimeVersion, &run.ZigVersion, &run.ManifestHash, &run.ManifestJSON); err != nil {
		return err
	}
	run.CommitHashFull = commitHashFull.String
	run.CommitMessage = commitMessage.String
	run.CommitDate = commitDate.String
	run.Branch = branch.String
	run.MachineID = machineID.String
	run.Notes = notes.String
	run.ZigOptimize = zigOptimize.String
	return nil
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
	ResultID    int64               `json:"-"`
	SampleIndex int64               `json:"sample_index"`
	AvgNs       int64               `json:"avg_ns"`
	InnerRSDPPM *int64              `json:"inner_rsd_ppm,omitempty"`
	Batches     []ResultSampleBatch `json:"batches,omitempty"`
}

type ResultSampleBatch struct {
	ResultID    int64 `json:"-"`
	SampleIndex int64 `json:"-"`
	BatchIndex  int64 `json:"batch_index"`
	ElapsedNs   int64 `json:"elapsed_ns"`
	Iterations  int64 `json:"iterations"`
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
	normalizeRunIdentity(run)
	res, err := db.Exec(`
		INSERT INTO runs (commit_hash, commit_hash_full, commit_message, commit_date, branch, run_date, machine_id, notes, zig_optimize,
			benchmark_kind, benchmark_suite, protocol_version, bun_version, js_runtime, runtime_version, zig_version, manifest_hash, manifest_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.CommitHash, run.CommitHashFull, run.CommitMessage, run.CommitDate,
		run.Branch, run.RunDate, run.MachineID, run.Notes, run.ZigOptimize,
		run.BenchmarkKind, run.BenchmarkSuite, run.ProtocolVersion, run.BunVersion, run.JSRuntime, run.RuntimeVersion,
		run.ZigVersion, run.ManifestHash, run.ManifestJSON)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func normalizeRunIdentity(run *Run) {
	if run.ZigOptimize == "" {
		run.ZigOptimize = "ReleaseFast"
	}
	if run.BenchmarkKind == "" {
		run.BenchmarkKind = "zig"
	}
	if run.BenchmarkSuite == "" {
		run.BenchmarkSuite = "core-default"
	}
	if run.ProtocolVersion == 0 {
		run.ProtocolVersion = 1
	}
	if run.BenchmarkKind == jsbench.Kind {
		if run.JSRuntime == "" {
			run.JSRuntime = jsbench.RuntimeBun
		}
		if run.RuntimeVersion == "" && run.JSRuntime == jsbench.RuntimeBun {
			run.RuntimeVersion = run.BunVersion
		}
		if run.BunVersion == "" && run.JSRuntime == jsbench.RuntimeBun {
			run.BunVersion = run.RuntimeVersion
		}
	}
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
	normalizeRunIdentity(run)
	tx, err := db.Begin()
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = tx.Rollback() }()

	runID, ids, _, err := insertRunWithResultsTx(tx, run, results, "")
	if err != nil {
		return 0, nil, err
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, err
	}
	return runID, ids, nil
}

// InsertRunWithResultsIfAbsent atomically persists a remote measurement once.
// Empty commit hashes are never deduplicated.
func (db *DB) InsertRunWithResultsIfAbsent(run *Run, results []Result) (*Run, map[BenchmarkKey]int64, bool, error) {
	normalizeRunIdentity(run)
	idempotencyKey := ""
	if run.CommitHashFull != "" {
		idempotencyKey = measurementIdempotencyKey(run)
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, nil, false, err
	}
	defer func() { _ = tx.Rollback() }()

	runID, ids, created, err := insertRunWithResultsTx(tx, run, results, idempotencyKey)
	if err != nil {
		return nil, nil, false, err
	}
	if !created {
		existing, err := getRunByIdempotencyKeyTx(tx, idempotencyKey)
		if err != nil {
			return nil, nil, false, err
		}
		ids, err := resultIDsForRunTx(tx, existing.ID)
		if err != nil {
			return nil, nil, false, err
		}
		return existing, ids, false, tx.Commit()
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, false, err
	}
	stored := *run
	stored.ID = runID
	return &stored, ids, true, nil
}

func insertRunWithResultsTx(tx *sql.Tx, run *Run, results []Result, idempotencyKey string) (int64, map[BenchmarkKey]int64, bool, error) {
	res, err := tx.Exec(`INSERT INTO runs (commit_hash, commit_hash_full, commit_message, commit_date, branch, run_date, machine_id, notes, zig_optimize,
		benchmark_kind, benchmark_suite, protocol_version, bun_version, js_runtime, runtime_version, zig_version, manifest_hash, manifest_json, idempotency_key)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''))
		ON CONFLICT(idempotency_key) WHERE idempotency_key IS NOT NULL AND idempotency_key <> '' DO NOTHING`, run.CommitHash, run.CommitHashFull, run.CommitMessage,
		run.CommitDate, run.Branch, run.RunDate, run.MachineID, run.Notes, run.ZigOptimize,
		run.BenchmarkKind, run.BenchmarkSuite, run.ProtocolVersion, run.BunVersion, run.JSRuntime, run.RuntimeVersion, run.ZigVersion, run.ManifestHash, run.ManifestJSON, idempotencyKey)
	if err != nil {
		return 0, nil, false, err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return 0, nil, false, err
	}
	if rows == 0 {
		return 0, nil, false, nil
	}
	runID, err := res.LastInsertId()
	if err != nil {
		return 0, nil, false, err
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
			return 0, nil, false, err
		}
		resultID, err := res.LastInsertId()
		if err != nil {
			return 0, nil, false, err
		}
		ids[BenchmarkKey{Category: result.Category, Name: result.Name}] = resultID
		for _, stat := range result.MemStats {
			if _, err := tx.Exec(`INSERT INTO mem_stats(result_id, stat_name, bytes) VALUES (?, ?, ?)`, resultID, stat.StatName, stat.Bytes); err != nil {
				return 0, nil, false, err
			}
		}
		for _, sample := range result.Samples {
			if _, err := tx.Exec(`INSERT INTO result_samples(result_id, sample_index, avg_ns, inner_rsd_ppm) VALUES (?, ?, ?, ?)`, resultID, sample.SampleIndex, sample.AvgNs, sample.InnerRSDPPM); err != nil {
				return 0, nil, false, err
			}
			for _, batch := range sample.Batches {
				if _, err := tx.Exec(`INSERT INTO result_sample_batches(result_id, sample_index, batch_index, elapsed_ns, iterations) VALUES (?, ?, ?, ?, ?)`,
					resultID, sample.SampleIndex, batch.BatchIndex, batch.ElapsedNs, batch.Iterations); err != nil {
					return 0, nil, false, err
				}
			}
		}
	}
	return runID, ids, true, nil
}

func measurementIdempotencyKey(run *Run) string {
	hash := sha256.New()
	branch := run.Branch
	if branch == "" || branch == "main" {
		branch = "main"
	}
	values := []string{idempotencySerialization(run), run.CommitHashFull, branch, run.MachineID, run.BenchmarkKind,
		run.BenchmarkSuite, fmt.Sprint(run.ProtocolVersion)}
	if values[0] == "measurement-v1" {
		values = append(values, run.BunVersion)
	} else {
		values = append(values, run.JSRuntime, run.RuntimeVersion)
	}
	values = append(values, run.ZigVersion, run.ManifestHash, relevantOptimize(run))
	for _, value := range values {
		_, _ = fmt.Fprintf(hash, "%d:%s", len(value), value)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func idempotencySerialization(run *Run) string {
	// Preserve keys used by already-deployed schema-1 Bun clients while all
	// generic runtime records use an identity that cannot collide across engines.
	if run.BenchmarkKind != jsbench.Kind || run.LegacyJSIdentity {
		return "measurement-v1"
	}
	return "measurement-v2"
}

func getRunByIdempotencyKeyTx(tx *sql.Tx, idempotencyKey string) (*Run, error) {
	var run Run
	var commitHashFullN, commitMessage, commitDate, branch, machineIDN, notes, zigOptimizeN sql.NullString
	err := tx.QueryRow(`
		SELECT id, commit_hash, commit_hash_full, commit_message, commit_date, branch, run_date, machine_id, notes, zig_optimize,
		       benchmark_kind, benchmark_suite, protocol_version, bun_version, js_runtime, runtime_version, zig_version, manifest_hash, manifest_json
		FROM runs WHERE idempotency_key = ?`, idempotencyKey).Scan(
		&run.ID, &run.CommitHash, &commitHashFullN, &commitMessage, &commitDate, &branch, &run.RunDate, &machineIDN, &notes, &zigOptimizeN,
		&run.BenchmarkKind, &run.BenchmarkSuite, &run.ProtocolVersion, &run.BunVersion, &run.JSRuntime, &run.RuntimeVersion, &run.ZigVersion, &run.ManifestHash, &run.ManifestJSON)
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

func relevantOptimize(run *Run) string {
	if run.BenchmarkKind == "zig" {
		return run.ZigOptimize
	}
	return ""
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
	return db.ListRunsFiltered(limit, branch, since, RunFilter{})
}

func (db *DB) ListRunsFiltered(limit int, branch string, since string, filter RunFilter) ([]Run, error) {
	query := `SELECT id, commit_hash, commit_hash_full, commit_message, commit_date, branch, run_date, machine_id, notes, zig_optimize,
		benchmark_kind, benchmark_suite, protocol_version, bun_version, js_runtime, runtime_version, zig_version, manifest_hash, manifest_json FROM runs WHERE 1=1`
	args := []interface{}{}

	if branch != "" {
		query += " AND branch = ?"
		args = append(args, branch)
	}
	if since != "" {
		query += " AND run_date >= ?"
		args = append(args, since)
	}
	query, args = appendRunFilter(query, args, "", filter)

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
		if err := scanRun(rows, &r); err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

func (db *DB) GetRun(id int64) (*Run, error) {
	var r Run
	err := scanRun(db.QueryRow(`
		SELECT id, commit_hash, commit_hash_full, commit_message, commit_date, branch, run_date, machine_id, notes, zig_optimize,
		       benchmark_kind, benchmark_suite, protocol_version, bun_version, js_runtime, runtime_version, zig_version, manifest_hash, manifest_json
		FROM runs WHERE id = ?`, id), &r)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (db *DB) GetRunByCommit(commitHash string) (*Run, error) {
	return db.GetRunByCommitFiltered(commitHash, RunFilter{})
}

func (db *DB) GetRunByCommitFiltered(commitHash string, filter RunFilter) (*Run, error) {
	var r Run
	query := `
		SELECT id, commit_hash, commit_hash_full, commit_message, commit_date, branch, run_date, machine_id, notes, zig_optimize,
		       benchmark_kind, benchmark_suite, protocol_version, bun_version, js_runtime, runtime_version, zig_version, manifest_hash, manifest_json
		FROM runs WHERE (commit_hash = ? OR commit_hash_full = ?)`
	args := []interface{}{commitHash, commitHash}
	query, args = appendRunFilter(query, args, "", filter)
	query += " ORDER BY run_date DESC, id DESC LIMIT 1"
	err := scanRun(db.QueryRow(query, args...), &r)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (db *DB) GetLatestRun() (*Run, error) {
	return db.GetLatestRunFiltered("", RunFilter{})
}

func (db *DB) GetLatestRunFiltered(branch string, filter RunFilter) (*Run, error) {
	var r Run
	query := `
		SELECT id, commit_hash, commit_hash_full, commit_message, commit_date, branch, run_date, machine_id, notes, zig_optimize,
		       benchmark_kind, benchmark_suite, protocol_version, bun_version, js_runtime, runtime_version, zig_version, manifest_hash, manifest_json
		FROM runs WHERE 1=1`
	args := []interface{}{}
	if branch == "main" {
		query += " AND (branch = 'main' OR branch IS NULL OR branch = '')"
	} else if branch != "" {
		query += " AND branch = ?"
		args = append(args, branch)
	}
	query, args = appendRunFilter(query, args, "", filter)
	query += " ORDER BY julianday(run_date) DESC, id DESC LIMIT 1"
	err := scanRun(db.QueryRow(query, args...), &r)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// GetLatestRunForBranch returns the most recent run for a given branch.
// An empty or "main" branch matches legacy NULL/empty values too.
func (db *DB) GetLatestRunForBranch(branch string) (*Run, error) {
	var query string
	var args []interface{}
	if branch == "main" || branch == "" {
		query = `
			SELECT id, commit_hash, commit_hash_full, commit_message, commit_date, branch, run_date, machine_id, notes, zig_optimize,
			       benchmark_kind, benchmark_suite, protocol_version, bun_version, js_runtime, runtime_version, zig_version, manifest_hash, manifest_json
			FROM runs
			WHERE branch = 'main' OR branch IS NULL OR branch = ''
			ORDER BY julianday(run_date) DESC, id DESC LIMIT 1`
	} else {
		query = `
			SELECT id, commit_hash, commit_hash_full, commit_message, commit_date, branch, run_date, machine_id, notes, zig_optimize,
			       benchmark_kind, benchmark_suite, protocol_version, bun_version, js_runtime, runtime_version, zig_version, manifest_hash, manifest_json
			FROM runs
			WHERE branch = ?
			ORDER BY julianday(run_date) DESC, id DESC LIMIT 1`
		args = append(args, branch)
	}

	var r Run
	err := scanRun(db.QueryRow(query, args...), &r)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// ListRunsForBranch returns runs for the requested branch ordered by newest first.
// "main" includes legacy NULL/empty branch values.
func (db *DB) ListRunsForBranch(branch string, limit int) ([]Run, error) {
	return db.ListRunsForBranchFiltered(branch, limit, RunFilter{})
}

func (db *DB) ListRunsForBranchFiltered(branch string, limit int, filter RunFilter) ([]Run, error) {
	query := `
		SELECT id, commit_hash, commit_hash_full, commit_message, commit_date, branch, run_date, machine_id, notes, zig_optimize,
		       benchmark_kind, benchmark_suite, protocol_version, bun_version, js_runtime, runtime_version, zig_version, manifest_hash, manifest_json
		FROM runs
		WHERE `
	args := []interface{}{}

	if branch == "main" || branch == "" {
		query += `(branch = 'main' OR branch IS NULL OR branch = '')`
	} else {
		query += `branch = ?`
		args = append(args, branch)
	}
	query, args = appendRunFilter(query, args, "", filter)

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
		if err := scanRun(rows, &r); err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}

	return runs, rows.Err()
}

// GetBranchesWithRuns returns distinct branch names that have at least one run,
// ordered with "main" first, then alphabetically. Legacy NULL/empty branches
// are normalized to "main".
func (db *DB) GetBranchesWithRuns() (branches []string, err error) {
	return db.GetBranchesWithRunsFiltered(RunFilter{})
}

func (db *DB) GetBranchesWithRunsFiltered(filter RunFilter) (branches []string, err error) {
	query := `
		SELECT DISTINCT CASE
			WHEN branch IS NULL OR branch = '' THEN 'main'
			ELSE branch
		END AS normalized_branch
		FROM runs WHERE 1=1`
	args := []interface{}{}
	query, args = appendRunFilter(query, args, "", filter)
	query += `
		ORDER BY
			CASE WHEN normalized_branch = 'main' THEN 0 ELSE 1 END,
			normalized_branch`
	rows, err := db.Query(query, args...)
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
	return db.HasCommitFiltered(commitHashFull, RunFilter{})
}

func (db *DB) HasCommitFiltered(commitHashFull string, filter RunFilter) (bool, error) {
	var count int
	query := `SELECT COUNT(*) FROM runs WHERE commit_hash_full = ?`
	args := []interface{}{commitHashFull}
	query, args = appendRunFilter(query, args, "", filter)
	err := db.QueryRow(query, args...).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (db *DB) GetResultsForRun(runID int64) ([]Result, error) {
	return db.getResultsForRun(runID, true)
}

// GetResultSummariesForRun omits memory, sample, and batch evidence. Callers
// doing run-level comparisons avoid two or more child queries per result.
func (db *DB) GetResultSummariesForRun(runID int64) ([]Result, error) {
	return db.getResultsForRun(runID, false)
}

func (db *DB) getResultsForRun(runID int64, includeEvidence bool) ([]Result, error) {
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

	if !includeEvidence {
		return results, nil
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
	rows, err := db.Query(`SELECT result_id, sample_index, avg_ns, inner_rsd_ppm FROM result_samples WHERE result_id = ? ORDER BY sample_index`, resultID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	samples := []ResultSample{}
	for rows.Next() {
		var sample ResultSample
		var rsd sql.NullInt64
		if err := rows.Scan(&sample.ResultID, &sample.SampleIndex, &sample.AvgNs, &rsd); err != nil {
			return nil, err
		}
		if rsd.Valid {
			value := rsd.Int64
			sample.InnerRSDPPM = &value
		}
		samples = append(samples, sample)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range samples {
		batches, err := db.GetResultSampleBatches(resultID, samples[i].SampleIndex)
		if err != nil {
			return nil, err
		}
		samples[i].Batches = batches
	}
	return samples, nil
}

func (db *DB) GetResultSampleBatches(resultID, sampleIndex int64) ([]ResultSampleBatch, error) {
	rows, err := db.Query(`SELECT result_id, sample_index, batch_index, elapsed_ns, iterations
		FROM result_sample_batches WHERE result_id = ? AND sample_index = ? ORDER BY batch_index`, resultID, sampleIndex)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	batches := []ResultSampleBatch{}
	for rows.Next() {
		var batch ResultSampleBatch
		if err := rows.Scan(&batch.ResultID, &batch.SampleIndex, &batch.BatchIndex, &batch.ElapsedNs, &batch.Iterations); err != nil {
			return nil, err
		}
		batches = append(batches, batch)
	}
	return batches, rows.Err()
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
	var refResult Result
	if err := db.QueryRow(`SELECT id, run_id, category, name FROM results WHERE id = ?`, resultID).Scan(
		&refResult.ID, &refResult.RunID, &refResult.Category, &refResult.Name); err != nil {
		return nil, err
	}
	refRun, err := db.GetRun(refResult.RunID)
	if err != nil {
		return nil, err
	}

	query := `
		SELECT 
			ru.id, ru.commit_hash, ru.commit_hash_full, ru.commit_message, ru.commit_date, ru.branch, ru.run_date, ru.machine_id, ru.notes, ru.zig_optimize,
			ru.benchmark_kind, ru.benchmark_suite, ru.protocol_version, ru.bun_version, ru.js_runtime, ru.runtime_version, ru.zig_version, ru.manifest_hash, ru.manifest_json,
			r.id, r.run_id, r.category, r.name, r.min_ns, r.avg_ns, r.max_ns, 
			COALESCE(r.std_dev_ns, 0), COALESCE(r.p50_ns, 0), COALESCE(r.p95_ns, 0), COALESCE(r.p99_ns, 0),
			r.total_ns, r.iterations, COALESCE(r.sample_count, 1)
		FROM results r
		JOIN runs ru ON r.run_id = ru.id
		WHERE r.category = ? AND r.name = ?`

	args := []interface{}{refResult.Category, refResult.Name}
	if !hasCompleteCohortIdentity(refRun) {
		query += " AND ru.id = ?"
		args = append(args, refRun.ID)
	} else {
		query, args = appendRunFilter(query, args, "ru", runCohortFilter(refRun))
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
			&run.BenchmarkKind, &run.BenchmarkSuite, &run.ProtocolVersion, &run.BunVersion, &run.JSRuntime, &run.RuntimeVersion, &run.ZigVersion, &run.ManifestHash, &run.ManifestJSON,
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

func hasCompleteCohortIdentity(run *Run) bool {
	if run.MachineID == "" || run.BenchmarkKind == "" || run.BenchmarkSuite == "" || run.ProtocolVersion <= 0 {
		return false
	}
	if run.BenchmarkKind == "zig" {
		return run.ZigOptimize != ""
	}
	return run.JSRuntime != "" && run.RuntimeVersion != "" && run.ZigVersion != "" && run.ManifestHash != ""
}

// CrossRuntimeCompatible reports whether JavaScript runs differ only in their
// measured runtime identity. It is intentionally descriptive, not inferential.
func CrossRuntimeCompatible(a, b *Run) bool {
	return a != nil && b != nil && a.BenchmarkKind == jsbench.Kind && b.BenchmarkKind == jsbench.Kind &&
		a.JSRuntime != b.JSRuntime && a.MachineID == b.MachineID && a.BenchmarkSuite == b.BenchmarkSuite &&
		a.ProtocolVersion == b.ProtocolVersion && a.ZigVersion == b.ZigVersion && a.ManifestHash == b.ManifestHash
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
		       ru.benchmark_kind, ru.benchmark_suite, ru.protocol_version,
		       ru.bun_version, ru.js_runtime, ru.runtime_version, ru.zig_version, ru.manifest_hash, ru.manifest_json,
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
		var benchmarkKind, benchmarkSuite, bunVersion, jsRuntime, runtimeVersion, zigVersion, manifestHash, manifestJSON string
		var protocolVersion int64
		var resultID, avgNs, stdDevNs, sampleCount sql.NullInt64
		var category, name sql.NullString
		if err := rows.Scan(
			&sourceRunID, &commitHash, &commitHashFull, &commitMessage, &commitDate,
			&branch, &runDate, &machineID, &notes, &zigOptimize,
			&benchmarkKind, &benchmarkSuite, &protocolVersion, &bunVersion, &jsRuntime, &runtimeVersion, &zigVersion, &manifestHash, &manifestJSON,
			&resultID, &category, &name, &avgNs, &stdDevNs, &sampleCount,
		); err != nil {
			return "", err
		}
		_, _ = fmt.Fprintf(hash, "%d:%q:%q:%q:%q:%q:%q:%q:%q:%q:%q:%q:%d:%q:%q:%q:%q:%q:%q:%d:%q:%q:%d:%d:%d\n",
			sourceRunID, commitHash, commitHashFull, commitMessage, commitDate, branch, runDate, machineID, notes, zigOptimize,
			benchmarkKind, benchmarkSuite, protocolVersion, bunVersion, jsRuntime, runtimeVersion, zigVersion, manifestHash, manifestJSON,
			resultID.Int64, category.String, name.String, avgNs.Int64, stdDevNs.Int64, sampleCount.Int64)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// RegressionDataFingerprints computes target-specific fingerprints with one
// causally ordered scan of the run history.
func (db *DB) RegressionDataFingerprints(runIDs []int64) (fingerprints map[int64]string, err error) {
	fingerprints = make(map[int64]string, len(runIDs))
	if len(runIDs) == 0 {
		return fingerprints, nil
	}

	uniqueIDs := make([]int64, 0, len(runIDs))
	seen := make(map[int64]struct{}, len(runIDs))
	for _, runID := range runIDs {
		if _, ok := seen[runID]; ok {
			continue
		}
		seen[runID] = struct{}{}
		uniqueIDs = append(uniqueIDs, runID)
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(uniqueIDs)), ",")
	args := make([]any, len(uniqueIDs))
	for i, runID := range uniqueIDs {
		args[i] = runID
	}
	targetRows, err := db.Query(`
		SELECT id, run_date, julianday(run_date) IS NOT NULL
		FROM runs
		WHERE id IN (`+placeholders+`)
		ORDER BY julianday(run_date), id`, args...)
	if err != nil {
		return nil, err
	}

	targets := make(map[int64]struct{}, len(uniqueIDs))
	emptyHash := sha256.Sum256(nil)
	var maxRunID int64
	var maxRunDate string
	for targetRows.Next() {
		var runID int64
		var runDate string
		var validDate bool
		if err := targetRows.Scan(&runID, &runDate, &validDate); err != nil {
			_ = targetRows.Close()
			return nil, err
		}
		targets[runID] = struct{}{}
		if !validDate {
			fingerprints[runID] = hex.EncodeToString(emptyHash[:])
			continue
		}
		maxRunID = runID
		maxRunDate = runDate
	}
	if err := targetRows.Err(); err != nil {
		_ = targetRows.Close()
		return nil, err
	}
	if err := targetRows.Close(); err != nil {
		return nil, err
	}
	if len(targets) != len(uniqueIDs) {
		return nil, sql.ErrNoRows
	}
	if maxRunDate == "" {
		return fingerprints, nil
	}

	rows, err := db.Query(`
		SELECT ru.id, ru.commit_hash, COALESCE(ru.commit_hash_full, ''),
		       COALESCE(ru.commit_message, ''), COALESCE(ru.commit_date, ''),
		       COALESCE(ru.branch, ''), ru.run_date, COALESCE(ru.machine_id, ''),
		       COALESCE(ru.notes, ''), COALESCE(ru.zig_optimize, ''),
		       ru.benchmark_kind, ru.benchmark_suite, ru.protocol_version,
		       ru.bun_version, ru.js_runtime, ru.runtime_version, ru.zig_version, ru.manifest_hash, ru.manifest_json,
		       r.id, r.category, r.name, r.avg_ns, r.std_dev_ns, r.sample_count
		FROM runs ru
		LEFT JOIN results r ON r.run_id = ru.id
		WHERE julianday(ru.run_date) < julianday(?)
		   OR (julianday(ru.run_date) = julianday(?) AND ru.id <= ?)
		ORDER BY julianday(ru.run_date), ru.id, r.category, r.name, r.id`,
		maxRunDate, maxRunDate, maxRunID)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	hash := sha256.New()
	var previousRunID int64
	for rows.Next() {
		var sourceRunID int64
		var commitHash, commitHashFull, commitMessage, commitDate, branch, runDate, machineID, notes, zigOptimize string
		var benchmarkKind, benchmarkSuite, bunVersion, jsRuntime, runtimeVersion, zigVersion, manifestHash, manifestJSON string
		var protocolVersion int64
		var resultID, avgNs, stdDevNs, sampleCount sql.NullInt64
		var category, name sql.NullString
		if err := rows.Scan(
			&sourceRunID, &commitHash, &commitHashFull, &commitMessage, &commitDate,
			&branch, &runDate, &machineID, &notes, &zigOptimize,
			&benchmarkKind, &benchmarkSuite, &protocolVersion, &bunVersion, &jsRuntime, &runtimeVersion, &zigVersion, &manifestHash, &manifestJSON,
			&resultID, &category, &name, &avgNs, &stdDevNs, &sampleCount,
		); err != nil {
			return nil, err
		}
		if previousRunID != 0 && sourceRunID != previousRunID {
			if _, ok := targets[previousRunID]; ok {
				fingerprints[previousRunID] = hex.EncodeToString(hash.Sum(nil))
			}
		}
		_, _ = fmt.Fprintf(hash, "%d:%q:%q:%q:%q:%q:%q:%q:%q:%q:%q:%q:%d:%q:%q:%q:%q:%q:%q:%d:%q:%q:%d:%d:%d\n",
			sourceRunID, commitHash, commitHashFull, commitMessage, commitDate, branch, runDate, machineID, notes, zigOptimize,
			benchmarkKind, benchmarkSuite, protocolVersion, bunVersion, jsRuntime, runtimeVersion, zigVersion, manifestHash, manifestJSON,
			resultID.Int64, category.String, name.String, avgNs.Int64, stdDevNs.Int64, sampleCount.Int64)
		previousRunID = sourceRunID
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if _, ok := targets[previousRunID]; ok {
		fingerprints[previousRunID] = hex.EncodeToString(hash.Sum(nil))
	}
	if len(fingerprints) != len(uniqueIDs) {
		return nil, fmt.Errorf("fingerprint scan resolved %d of %d target runs", len(fingerprints), len(uniqueIDs))
	}
	return fingerprints, nil
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

type ProfileRetention struct {
	MaxRuns  int
	MaxBytes int64
}

type ProfileRetentionResult struct {
	BlobsDeleted        int64
	BytesDeleted        int64
	ProfileRunsRetained int
	BytesRetained       int64
}

type profileStorageQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

func profileStorageStats(q profileStorageQuerier) (blobs int64, bytes int64, err error) {
	err = q.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(size_bytes), 0)
		FROM (
			SELECT length(data_blob) AS size_bytes
			FROM artifacts
			WHERE kind IN ('cpu.pprof', 'cpu.flamegraph.svg', 'cpu.callgraph.svg')
			UNION ALL
			SELECT length(folded_stacks_gz) AS size_bytes FROM flamegraphs
		)`).Scan(&blobs, &bytes)
	return blobs, bytes, err
}

func retainedProfileRunIDs(tx *sql.Tx, retention ProfileRetention) ([]int64, int64, error) {
	rows, err := tx.Query(`
		SELECT r.run_id, SUM(length(a.data_blob)) AS size_bytes
		FROM artifacts a
		JOIN results r ON r.id = a.result_id
		JOIN runs ON runs.id = r.run_id
		WHERE a.kind = 'cpu.pprof'
		GROUP BY r.run_id
		HAVING COUNT(*) = (SELECT COUNT(*) FROM results expected WHERE expected.run_id = r.run_id)
		ORDER BY julianday(runs.run_date) DESC, r.run_id DESC`)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	ids := make([]int64, 0, retention.MaxRuns)
	var retainedBytes int64
	for rows.Next() {
		var runID, sizeBytes int64
		if err := rows.Scan(&runID, &sizeBytes); err != nil {
			return nil, 0, err
		}
		if len(ids) >= retention.MaxRuns || sizeBytes > retention.MaxBytes-retainedBytes {
			break
		}
		ids = append(ids, runID)
		retainedBytes += sizeBytes
	}
	return ids, retainedBytes, rows.Err()
}

func deleteUnretainedProfiles(tx *sql.Tx, retainedRunIDs []int64, preserveIncomplete bool) error {
	if _, err := tx.Exec(`DELETE FROM flamegraphs`); err != nil {
		return err
	}

	runFilter := ""
	args := make([]any, len(retainedRunIDs))
	if len(retainedRunIDs) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(retainedRunIDs)), ",")
		runFilter = " AND run_id NOT IN (" + placeholders + ")"
		for i, runID := range retainedRunIDs {
			args[i] = runID
		}
	}
	if preserveIncomplete {
		runFilter += ` AND run_id IN (
			SELECT expected.run_id
			FROM results expected
			LEFT JOIN artifacts profile
			  ON profile.result_id = expected.id AND profile.kind = 'cpu.pprof'
			GROUP BY expected.run_id
			HAVING COUNT(profile.id) = COUNT(*)
		)`
	}
	if _, err := tx.Exec(`
		DELETE FROM artifacts
		WHERE kind = 'cpu.pprof'
		  AND result_id IN (
			SELECT id FROM results WHERE 1 = 1`+runFilter+`
		  )`, args...); err != nil {
		return err
	}
	return nil
}

func finalizeProfileRun(tx *sql.Tx, runID int64) (bool, error) {
	var resultCount, profileCount int64
	if err := tx.QueryRow(`
		SELECT COUNT(*), COUNT(profile.id)
		FROM results result
		LEFT JOIN artifacts profile
		  ON profile.result_id = result.id AND profile.kind = 'cpu.pprof'
		WHERE result.run_id = ?`, runID).Scan(&resultCount, &profileCount); err != nil {
		return false, err
	}
	if resultCount == 0 {
		return false, fmt.Errorf("run %d has no results", runID)
	}
	if profileCount == resultCount {
		return true, nil
	}
	_, err := tx.Exec(`
		DELETE FROM artifacts
		WHERE kind = 'cpu.pprof'
		  AND result_id IN (SELECT id FROM results WHERE run_id = ?)`, runID)
	return false, err
}

// PruneProfileData preserves compact benchmark summaries while bounding bulky
// source profiles. Derived SVG artifacts are always removed because they can be
// regenerated into the bounded filesystem cache.
func (db *DB) pruneProfileData(retention ProfileRetention, finalizeRunID int64) (ProfileRetentionResult, bool, error) {
	if retention.MaxRuns <= 0 {
		return ProfileRetentionResult{}, false, fmt.Errorf("profile retention max runs must be positive")
	}
	if retention.MaxBytes <= 0 {
		return ProfileRetentionResult{}, false, fmt.Errorf("profile retention max bytes must be positive")
	}

	tx, err := db.Begin()
	if err != nil {
		return ProfileRetentionResult{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	beforeBlobs, beforeBytes, err := profileStorageStats(tx)
	if err != nil {
		return ProfileRetentionResult{}, false, err
	}
	complete := true
	if finalizeRunID != 0 {
		complete, err = finalizeProfileRun(tx, finalizeRunID)
		if err != nil {
			return ProfileRetentionResult{}, false, err
		}
	}
	retainedRunIDs, _, err := retainedProfileRunIDs(tx, retention)
	if err != nil {
		return ProfileRetentionResult{}, false, err
	}
	if _, err := tx.Exec(`DELETE FROM artifacts WHERE kind IN ('cpu.flamegraph.svg', 'cpu.callgraph.svg')`); err != nil {
		return ProfileRetentionResult{}, false, err
	}
	if err := deleteUnretainedProfiles(tx, retainedRunIDs, finalizeRunID != 0); err != nil {
		return ProfileRetentionResult{}, false, err
	}
	afterBlobs, afterBytes, err := profileStorageStats(tx)
	if err != nil {
		return ProfileRetentionResult{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return ProfileRetentionResult{}, false, err
	}
	return ProfileRetentionResult{
		BlobsDeleted:        beforeBlobs - afterBlobs,
		BytesDeleted:        beforeBytes - afterBytes,
		ProfileRunsRetained: len(retainedRunIDs),
		BytesRetained:       afterBytes,
	}, complete, nil
}

func (db *DB) PruneProfileData(retention ProfileRetention) (ProfileRetentionResult, error) {
	result, _, err := db.pruneProfileData(retention, 0)
	return result, err
}

// FinalizeProfileData applies retention after a run's uploads. Other incomplete
// runs are preserved because they may still be uploading concurrently. An
// incomplete target run is discarded as a unit.
func (db *DB) FinalizeProfileData(runID int64, retention ProfileRetention) (ProfileRetentionResult, bool, error) {
	return db.pruneProfileData(retention, runID)
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
		SELECT id, commit_hash, commit_hash_full, commit_message, commit_date, branch, run_date, machine_id, notes, zig_optimize,
		       benchmark_kind, benchmark_suite, protocol_version, bun_version, js_runtime, runtime_version, zig_version, manifest_hash, manifest_json
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

	if !hasCompleteCohortIdentity(refRun) {
		query += ` AND id = ?`
		args = append(args, refRun.ID)
	} else {
		query, args = appendRunFilter(query, args, "", runCohortFilter(refRun))
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
		if err := scanRun(rows, &r); err != nil {
			return nil, err
		}
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
	if !hasCompleteCohortIdentity(refRun) {
		return []Run{}, nil
	}

	query := `
		SELECT id, commit_hash, commit_hash_full, commit_message, commit_date, branch, run_date, machine_id, notes, zig_optimize,
		       benchmark_kind, benchmark_suite, protocol_version, bun_version, js_runtime, runtime_version, zig_version, manifest_hash, manifest_json
		FROM runs
		WHERE (branch = 'main' OR branch IS NULL OR branch = '')`
	query, args := appendRunFilter(query, nil, "", runCohortFilter(refRun))
	query += ` AND (julianday(run_date) < julianday(?) OR (julianday(run_date) = julianday(?) AND id <= ?))
		ORDER BY julianday(run_date) DESC, id DESC
		LIMIT ?`
	args = append(args, refRun.RunDate, refRun.RunDate, refRun.ID, window)
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var runs []Run
	for rows.Next() {
		var r Run
		if err := scanRun(rows, &r); err != nil {
			return nil, err
		}
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
	ID              int64
	Status          string // pending, running, completed, failed, cancelled
	Kind            string // benchmark
	Branch          string
	CommitHash      string // optional: specific commit, empty = branch HEAD
	RepoURL         string // git remote name or URL
	Samples         int
	Profile         string // none, cpu
	Notes           string
	CreatedAt       string
	StartedAt       string
	CompletedAt     string
	Error           string
	RunID           *int64 // links to resulting benchmark run
	RequestedBy     string
	ClaimToken      string // populated only by ClaimNextPendingJob
	BenchmarkKind   string
	BenchmarkSuite  string
	ProtocolVersion int64
	ManifestHash    string
	JSRuntime       string
	RuntimeVersion  string
}

func (db *DB) InsertJob(job *Job) (int64, error) {
	if job.BenchmarkKind == "" {
		job.BenchmarkKind = "zig"
	}
	if job.BenchmarkSuite == "" {
		job.BenchmarkSuite = "core-default"
	}
	if job.ProtocolVersion == 0 {
		job.ProtocolVersion = 1
	}
	if job.BenchmarkKind == jsbench.Kind {
		if job.JSRuntime == "" {
			job.JSRuntime = jsbench.RuntimeBun
		}
		if job.RuntimeVersion == "" {
			job.RuntimeVersion = jsbench.RuntimeVersion(job.JSRuntime)
		}
	}
	if job.BenchmarkKind != "zig" && job.BenchmarkKind != jsbench.Kind {
		return 0, fmt.Errorf("benchmark kind must be zig or js")
	}
	if job.BenchmarkKind == jsbench.Kind && !jsbench.MatchesRuntimeJob(job.BenchmarkSuite, job.ProtocolVersion,
		job.JSRuntime, job.RuntimeVersion, job.ManifestHash, job.Samples, job.Profile) {
		return 0, fmt.Errorf("JavaScript jobs require canonical suite, protocol, manifest, three samples, and no profile")
	}
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
		INSERT INTO jobs (status, kind, branch, commit_hash, repo_url, samples, profile, notes, created_at, requested_by,
			benchmark_kind, benchmark_suite, protocol_version, manifest_hash, js_runtime, runtime_version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.Status, job.Kind, job.Branch, commitHash, job.RepoURL,
		job.Samples, job.Profile, notes, job.CreatedAt, requestedBy,
		job.BenchmarkKind, job.BenchmarkSuite, job.ProtocolVersion, job.ManifestHash, job.JSRuntime, job.RuntimeVersion)
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
		       created_at, started_at, completed_at, error, run_id, requested_by,
		       benchmark_kind, benchmark_suite, protocol_version, manifest_hash, js_runtime, runtime_version
		FROM jobs WHERE id = ?`, id).Scan(
		&j.ID, &j.Status, &j.Kind, &j.Branch, &commitHash, &j.RepoURL,
		&j.Samples, &j.Profile, &notes,
		&j.CreatedAt, &startedAt, &completedAt, &jobError, &runID, &requestedBy,
		&j.BenchmarkKind, &j.BenchmarkSuite, &j.ProtocolVersion, &j.ManifestHash, &j.JSRuntime, &j.RuntimeVersion)
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
	return db.ListJobsFiltered(limit, status, branch, "", "", "", 0, "")
}

func (db *DB) ListJobsFiltered(limit int, status string, branch string, benchmarkKind string, requestedBy string,
	benchmarkSuite string, protocolVersion int64, manifestHash string, runtimeIdentity ...string,
) ([]Job, error) {
	query := `SELECT id, status, kind, branch, commit_hash, repo_url, samples, profile, notes,
	                 created_at, started_at, completed_at, error, run_id, requested_by,
	                 benchmark_kind, benchmark_suite, protocol_version, manifest_hash, js_runtime, runtime_version
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
	if benchmarkKind != "" {
		query += " AND benchmark_kind = ?"
		args = append(args, benchmarkKind)
	}
	if requestedBy != "" {
		query += " AND requested_by = ?"
		args = append(args, requestedBy)
	}
	if benchmarkSuite != "" {
		query += " AND benchmark_suite = ?"
		args = append(args, benchmarkSuite)
	}
	if protocolVersion > 0 {
		query += " AND protocol_version = ?"
		args = append(args, protocolVersion)
	}
	if manifestHash != "" {
		query += " AND manifest_hash = ?"
		args = append(args, manifestHash)
	}
	if len(runtimeIdentity) > 0 && runtimeIdentity[0] != "" {
		query += " AND js_runtime = ?"
		args = append(args, runtimeIdentity[0])
	}
	if len(runtimeIdentity) > 1 && runtimeIdentity[1] != "" {
		query += " AND runtime_version = ?"
		args = append(args, runtimeIdentity[1])
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
			&j.BenchmarkKind, &j.BenchmarkSuite, &j.ProtocolVersion, &j.ManifestHash, &j.JSRuntime, &j.RuntimeVersion,
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

// JobLeaseDuration bounds how long an unresponsive worker can retain a job.
// The longest observed jobs (Zig suite with cpu profiling) finish within
// about 30 minutes, so two hours leaves ample margin while letting jobs
// stranded by worker crashes recover on the next claim instead of after a day.
const JobLeaseDuration = 2 * time.Hour

var (
	ErrJobClaimLost   = errors.New("job claim is no longer active")
	ErrJobRunMismatch = errors.New("run does not match job commit and benchmark identity")
)

func hashJobClaimToken(token string) string {
	return joblease.HashToken(token)
}

// Empty credentials are reserved for workers that were already running when
// claim tokens were introduced. Every job claimed under the new protocol has a
// non-empty digest, including legacy jobs after stale recovery.
func storedJobClaimCredential(token string) string {
	if token == "" {
		return ""
	}
	return hashJobClaimToken(token)
}

// ClaimNextPendingJob atomically recovers expired running jobs, then finds the
// oldest matching pending job and sets it to running. An empty benchmark kind
// matches either kind. Returns nil, nil if no matching pending jobs exist.
func (db *DB) ClaimNextPendingJob(benchmarkKind string) (*Job, error) {
	claimToken, err := joblease.NewToken()
	if err != nil {
		return nil, fmt.Errorf("generate job claim token: %w", err)
	}
	return db.ClaimNextPendingJobWithToken(benchmarkKind, claimToken, jsbench.RuntimeBun)
}

// ClaimNextPendingJobWithToken uses a caller-owned bearer token so a repeated
// remote claim can recover the lease after its first response was lost.
func (db *DB) ClaimNextPendingJobWithToken(benchmarkKind, claimToken string, javascriptRuntimes ...string) (*Job, error) {
	if benchmarkKind != "" && benchmarkKind != "zig" && benchmarkKind != jsbench.Kind {
		return nil, fmt.Errorf("benchmark kind must be zig or js")
	}
	if err := joblease.ValidateToken(claimToken); err != nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	nowTime := time.Now().UTC()
	now := nowTime.Format(time.RFC3339)
	staleBefore := nowTime.Add(-JobLeaseDuration).Format(time.RFC3339)
	if _, err := tx.Exec(`
		UPDATE jobs SET status = 'pending', started_at = NULL, claim_token = NULL, legacy_tokenless = 0
		WHERE status = 'running'
		  AND (started_at IS NULL OR julianday(started_at) <= julianday(?))`, staleBefore); err != nil {
		return nil, err
	}

	claimDigest := hashJobClaimToken(claimToken)
	var existingID int64
	var existingKind string
	err = tx.QueryRow(`SELECT id, benchmark_kind FROM jobs WHERE status = 'running' AND claim_token = ?`, claimDigest).
		Scan(&existingID, &existingKind)
	if err == nil {
		if benchmarkKind != "" && benchmarkKind != existingKind {
			return nil, fmt.Errorf("claim token already owns a %s job", existingKind)
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		job, err := db.GetJob(existingID)
		if err != nil {
			return nil, err
		}
		job.ClaimToken = claimToken
		return job, nil
	}
	if err != sql.ErrNoRows {
		return nil, err
	}

	query := `SELECT id FROM jobs WHERE status = 'pending'`
	args := []interface{}{}
	if benchmarkKind != "" {
		query += ` AND benchmark_kind = ?`
		args = append(args, benchmarkKind)
	}
	if len(javascriptRuntimes) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(javascriptRuntimes)), ",")
		query += ` AND (benchmark_kind != 'js' OR js_runtime IN (` + placeholders + `))`
		for _, runtime := range javascriptRuntimes {
			if jsbench.RuntimeVersion(runtime) == "" {
				return nil, fmt.Errorf("unsupported JavaScript runtime %q", runtime)
			}
			args = append(args, runtime)
		}
	} else {
		query += ` AND benchmark_kind != 'js'`
	}
	query += ` ORDER BY created_at ASC LIMIT 1`
	var jobID int64
	err = tx.QueryRow(query, args...).Scan(&jobID)
	if err == sql.ErrNoRows {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	res, err := tx.Exec(`UPDATE jobs SET status = 'running', started_at = ?, claim_token = ?, legacy_tokenless = 0 WHERE id = ? AND status = 'pending'`, now, claimDigest, jobID)
	if err != nil {
		return nil, err
	}
	if err := requireActiveJobClaim(res, jobID); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	job, err := db.GetJob(jobID)
	if err != nil {
		return nil, err
	}
	job.ClaimToken = claimToken
	return job, nil
}

// CompleteJob marks the actively claimed job as completed and links the resulting run.
func (db *DB) CompleteJob(ctx context.Context, jobID int64, claimToken string, runID int64) error {
	result, err := db.ExecContext(ctx, `
		UPDATE jobs
		SET status = 'completed', completed_at = ?, run_id = ?, error = NULL, claim_token = NULL, legacy_tokenless = 0
		WHERE id = ? AND status = 'running' AND COALESCE(claim_token, '') = ?
		  AND (? <> '' OR legacy_tokenless = 1) AND jobs.commit_hash <> ''
		  AND EXISTS (
			SELECT 1 FROM runs
			WHERE runs.id = ?
			  AND COALESCE(NULLIF(runs.commit_hash_full, ''), runs.commit_hash) = jobs.commit_hash
			  AND runs.benchmark_kind = jobs.benchmark_kind
			  AND runs.benchmark_suite = jobs.benchmark_suite
			  AND runs.protocol_version = jobs.protocol_version
			  AND runs.manifest_hash = jobs.manifest_hash
			  AND runs.js_runtime = jobs.js_runtime
			  AND runs.runtime_version = jobs.runtime_version
		  )`, timeNow(), runID, jobID, storedJobClaimCredential(claimToken), claimToken, runID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return nil
	}

	var active bool
	if err := db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM jobs WHERE id = ? AND status = 'running' AND COALESCE(claim_token, '') = ?
			  AND (? <> '' OR legacy_tokenless = 1)
		)`, jobID, storedJobClaimCredential(claimToken), claimToken).Scan(&active); err != nil {
		return err
	}
	if active {
		return fmt.Errorf("%w for job %d and run %d", ErrJobRunMismatch, jobID, runID)
	}
	return fmt.Errorf("%w for job %d", ErrJobClaimLost, jobID)
}

// FailJob marks the actively claimed job as failed with an error message.
func (db *DB) FailJob(ctx context.Context, jobID int64, claimToken, errMsg string) error {
	const maxJobErrorBytes = 4096
	if len(errMsg) > maxJobErrorBytes {
		errMsg = errMsg[:maxJobErrorBytes]
		for !utf8.ValidString(errMsg) {
			errMsg = errMsg[:len(errMsg)-1]
		}
	}
	return db.updateClaimedJob(ctx, jobID, claimToken,
		`status = 'failed', completed_at = ?, error = ?, claim_token = NULL, legacy_tokenless = 0`, timeNow(), errMsg)
}

// ReleaseJob returns the actively claimed job to the queue for another worker.
func (db *DB) ReleaseJob(ctx context.Context, jobID int64, claimToken string) error {
	return db.updateClaimedJob(ctx, jobID, claimToken,
		`status = 'pending', started_at = NULL, claim_token = NULL, legacy_tokenless = 0`)
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

// UpdateJobCommitHash sets the resolved commit hash on the actively claimed job.
func (db *DB) UpdateJobCommitHash(ctx context.Context, jobID int64, claimToken, commitHash string) error {
	return db.updateClaimedJob(ctx, jobID, claimToken, `commit_hash = ?`, commitHash)
}

func (db *DB) updateClaimedJob(ctx context.Context, jobID int64, claimToken, setClause string, args ...any) error {
	args = append(args, jobID, storedJobClaimCredential(claimToken), claimToken)
	result, err := db.ExecContext(ctx, `UPDATE jobs SET `+setClause+
		` WHERE id = ? AND status = 'running' AND COALESCE(claim_token, '') = ?
		  AND (? <> '' OR legacy_tokenless = 1)`, args...)
	if err != nil {
		return err
	}
	return requireActiveJobClaim(result, jobID)
}

func requireActiveJobClaim(result sql.Result, jobID int64) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("%w for job %d", ErrJobClaimLost, jobID)
	}
	return nil
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
