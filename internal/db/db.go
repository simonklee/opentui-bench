package db

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

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
    sample_count INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX IF NOT EXISTS idx_results_run ON results(run_id);
CREATE INDEX IF NOT EXISTS idx_results_name ON results(name);
CREATE INDEX IF NOT EXISTS idx_results_category ON results(category);

CREATE TABLE IF NOT EXISTS mem_stats (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    result_id INTEGER NOT NULL REFERENCES results(id) ON DELETE CASCADE,
    stat_name TEXT NOT NULL,
    bytes INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_mem_stats_result ON mem_stats(result_id);

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
`

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
		sqlDB.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	if _, err := sqlDB.Exec("PRAGMA foreign_keys = ON"); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	database := &DB{DB: sqlDB, path: dbPath}

	if err := database.migrate(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}

	if _, err := sqlDB.Exec(schemaSQL); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("initialize schema: %w", err)
	}

	return database, nil
}

func (db *DB) migrate() error {
	var tableName string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='flamegraphs'`).Scan(&tableName)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}

	hasOldSchema, err := db.checkOldFlamegraphSchema()
	if err != nil {
		return err
	}
	if !hasOldSchema {
		return nil
	}

	fmt.Println("Migrating flamegraphs table to compressed format...")

	oldData, err := db.readOldFlamegraphs()
	if err != nil {
		return err
	}

	if err := db.performFlamegraphMigration(oldData); err != nil {
		return err
	}

	if _, err := db.Exec(`VACUUM`); err != nil {
		fmt.Printf("Warning: VACUUM failed: %v\n", err)
	}

	fmt.Printf("Migrated %d flamegraphs to compressed format\n", len(oldData))
	return nil
}

func (db *DB) checkOldFlamegraphSchema() (bool, error) {
	rows, err := db.Query(`PRAGMA table_info(flamegraphs)`)
	if err != nil {
		return false, err
	}

	var hasOldSchema bool
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dfltValue sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			rows.Close()
			return false, err
		}
		if name == "folded_stacks" || name == "svg" {
			hasOldSchema = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}
	return hasOldSchema, nil
}

type oldFlamegraph struct {
	id            int64
	runID         int64
	benchmarkName string
	foldedStacks  string
	samplingFreq  int
	createdAt     string
}

func (db *DB) readOldFlamegraphs() ([]oldFlamegraph, error) {
	rows, err := db.Query(`SELECT id, run_id, benchmark_name, folded_stacks, sampling_freq, created_at FROM flamegraphs`)
	if err != nil {
		return nil, fmt.Errorf("read old flamegraphs: %w", err)
	}

	var oldData []oldFlamegraph
	for rows.Next() {
		var fg oldFlamegraph
		if err := rows.Scan(&fg.id, &fg.runID, &fg.benchmarkName, &fg.foldedStacks, &fg.samplingFreq, &fg.createdAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan old flamegraph: %w", err)
		}
		oldData = append(oldData, fg)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read old flamegraphs: %w", err)
	}
	return oldData, nil
}

func (db *DB) performFlamegraphMigration(oldData []oldFlamegraph) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DROP TABLE flamegraphs`); err != nil {
		return fmt.Errorf("drop old table: %w", err)
	}

	createSQL := `CREATE TABLE flamegraphs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		run_id INTEGER NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
		benchmark_name TEXT NOT NULL,
		folded_stacks_gz BLOB NOT NULL,
		sampling_freq INTEGER NOT NULL DEFAULT 997,
		created_at TEXT NOT NULL
	)`
	if _, err := tx.Exec(createSQL); err != nil {
		return fmt.Errorf("create new table: %w", err)
	}

	if _, err := tx.Exec(`CREATE UNIQUE INDEX idx_flamegraphs_run_benchmark ON flamegraphs(run_id, benchmark_name)`); err != nil {
		return fmt.Errorf("create index: %w", err)
	}

	for _, fg := range oldData {
		compressed, err := gzipCompress([]byte(fg.foldedStacks))
		if err != nil {
			return fmt.Errorf("compress flamegraph %s: %w", fg.benchmarkName, err)
		}
		if _, err := tx.Exec(`INSERT INTO flamegraphs (run_id, benchmark_name, folded_stacks_gz, sampling_freq, created_at) VALUES (?, ?, ?, ?, ?)`,
			fg.runID, fg.benchmarkName, compressed, fg.samplingFreq, fg.createdAt); err != nil {
			return fmt.Errorf("insert flamegraph %s: %w", fg.benchmarkName, err)
		}
	}

	return tx.Commit()
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

type Result struct {
	ID          int64
	RunID       int64
	Category    string
	Name        string
	MinNs       int64
	AvgNs       int64
	MaxNs       int64
	StdDevNs    int64
	P50Ns       int64
	P95Ns       int64
	P99Ns       int64
	TotalNs     int64
	Iterations  int64
	SampleCount int64
	MemStats    []MemStat
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
	defer r.Close()
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
		INSERT INTO results (run_id, category, name, min_ns, avg_ns, max_ns, std_dev_ns, p50_ns, p95_ns, p99_ns, total_ns, iterations, sample_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		result.RunID, result.Category, result.Name,
		result.MinNs, result.AvgNs, result.MaxNs, result.StdDevNs,
		result.P50Ns, result.P95Ns, result.P99Ns,
		result.TotalNs, result.Iterations, result.SampleCount)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
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
	defer rows.Close()

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
		       total_ns, iterations, COALESCE(sample_count, 1)
		FROM results WHERE run_id = ? ORDER BY category, name`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []Result
	for rows.Next() {
		var r Result
		if err := rows.Scan(&r.ID, &r.RunID, &r.Category, &r.Name, &r.MinNs, &r.AvgNs, &r.MaxNs,
			&r.StdDevNs, &r.P50Ns, &r.P95Ns, &r.P99Ns,
			&r.TotalNs, &r.Iterations, &r.SampleCount); err != nil {
			return nil, err
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
	}

	return results, nil
}

func (db *DB) GetResult(resultID int64) (*Result, error) {
	var r Result
	err := db.QueryRow(`
		SELECT id, run_id, category, name, min_ns, avg_ns, max_ns,
		       COALESCE(std_dev_ns, 0), COALESCE(p50_ns, 0), COALESCE(p95_ns, 0), COALESCE(p99_ns, 0),
		       total_ns, iterations, COALESCE(sample_count, 1)
		FROM results WHERE id = ?`, resultID).Scan(
		&r.ID, &r.RunID, &r.Category, &r.Name, &r.MinNs, &r.AvgNs, &r.MaxNs,
		&r.StdDevNs, &r.P50Ns, &r.P95Ns, &r.P99Ns,
		&r.TotalNs, &r.Iterations, &r.SampleCount)
	if err != nil {
		return nil, err
	}
	return &r, nil
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
	defer rows.Close()

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
		WHERE r.run_id = ? AND (a.id IS NOT NULL OR f.id IS NOT NULL)
		ORDER BY r.category, r.name`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

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
	defer rows.Close()

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

func (db *DB) GetTrend(namePattern string, limit int) ([]struct {
	Run    Run
	Result Result
}, error,
) {
	query := `
		SELECT 
			ru.id, ru.commit_hash, ru.commit_hash_full, ru.commit_message, ru.commit_date, ru.branch, ru.run_date, ru.machine_id, ru.notes, ru.zig_optimize,
			r.id, r.run_id, r.category, r.name, r.min_ns, r.avg_ns, r.max_ns, 
			COALESCE(r.std_dev_ns, 0), COALESCE(r.p50_ns, 0), COALESCE(r.p95_ns, 0), COALESCE(r.p99_ns, 0),
			r.total_ns, r.iterations, COALESCE(r.sample_count, 1)
		FROM results r
		JOIN runs ru ON r.run_id = ru.id
		WHERE r.name LIKE ?
		ORDER BY ru.run_date DESC`

	args := []interface{}{"%" + namePattern + "%"}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

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
	_, err := db.Exec(`DELETE FROM runs WHERE id = ?`, id)
	return err
}

func (db *DB) DeleteRunsBefore(date string) (int64, error) {
	res, err := db.Exec(`DELETE FROM runs WHERE run_date < ?`, date)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
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
	defer rows.Close()

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
	defer rows.Close()

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
	defer rows.Close()

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
// Comparable means same branch, machine_id, and zig_optimize.
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
		WHERE (branch = ? OR (branch IS NULL AND ? = ''))
		  AND (machine_id = ? OR (machine_id IS NULL AND ? = ''))
		  AND (zig_optimize = ? OR (zig_optimize IS NULL AND ? = ''))
		  AND run_date <= ?
		ORDER BY run_date DESC
		LIMIT ?`

	rows, err := db.Query(query,
		refRun.Branch, refRun.Branch,
		refRun.MachineID, refRun.MachineID,
		refRun.ZigOptimize, refRun.ZigOptimize,
		refRun.RunDate,
		window)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

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

// GetResultsForBenchmarkInRuns fetches all results for a specific benchmark name across multiple runs.
// Returns a map of runID -> Result.
func (db *DB) GetResultsForBenchmarkInRuns(benchmarkName string, runIDs []int64) (map[int64]Result, error) {
	if len(runIDs) == 0 {
		return make(map[int64]Result), nil
	}

	// Build placeholders for IN clause
	placeholders := make([]string, len(runIDs))
	args := make([]interface{}, len(runIDs)+1)
	args[0] = benchmarkName
	for i, id := range runIDs {
		placeholders[i] = "?"
		args[i+1] = id
	}

	query := fmt.Sprintf(`
		SELECT id, run_id, category, name, min_ns, avg_ns, max_ns,
		       COALESCE(std_dev_ns, 0), COALESCE(p50_ns, 0), COALESCE(p95_ns, 0), COALESCE(p99_ns, 0),
		       total_ns, iterations, COALESCE(sample_count, 1)
		FROM results
		WHERE name = ? AND run_id IN (%s)`, strings.Join(placeholders, ","))

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

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

// GetDistinctBenchmarkNames returns all unique benchmark names from a set of runs.
func (db *DB) GetDistinctBenchmarkNames(runIDs []int64) ([]string, error) {
	if len(runIDs) == 0 {
		return []string{}, nil
	}

	placeholders := make([]string, len(runIDs))
	args := make([]interface{}, len(runIDs))
	for i, id := range runIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	query := fmt.Sprintf(`
		SELECT DISTINCT name FROM results
		WHERE run_id IN (%s)
		ORDER BY name`, strings.Join(placeholders, ","))

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}
