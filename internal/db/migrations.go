package db

import (
	"database/sql"
	"fmt"
)

type migration func(*sql.Tx) error

// Versions are intentionally coarse milestones. generation_key contains the
// regression algorithm, detector configuration, cohort policy, and data
// fingerprint; version 3 replaces older cache layouts and purges their rows.
var migrations = []migration{
	migrateLegacySchema,
	migrateCompositeIdentity,
	migrateRegressionCache,
	migrateStoragePrecision,
}

func (db *DB) migrate() error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version > CurrentSchemaVersion {
		return fmt.Errorf("database schema version %d is newer than supported version %d", version, CurrentSchemaVersion)
	}

	var tables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`).Scan(&tables); err != nil {
		return err
	}
	if version == 0 && tables == 0 {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := tx.Exec(schemaSQL); err != nil {
			return fmt.Errorf("initialize schema: %w", err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, CurrentSchemaVersion)); err != nil {
			return err
		}
		return tx.Commit()
	}

	for version < CurrentSchemaVersion {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		next := version + 1
		if err := migrations[version](tx); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("schema migration %d: %w", next, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, next)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("set schema version %d: %w", next, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit schema migration %d: %w", next, err)
		}
		version = next
	}
	return nil
}

func migrateLegacySchema(tx *sql.Tx) error {
	resultsExists, err := tableExists(tx, "results")
	if err != nil {
		return err
	}
	if resultsExists {
		if err := auditResultIdentity(tx); err != nil {
			return err
		}
	}
	columns, err := tableColumns(tx, "flamegraphs")
	if err != nil {
		return err
	}
	if columns["svg"] && !columns["folded_stacks"] && !columns["folded_stacks_gz"] {
		return fmt.Errorf("legacy flamegraphs table has svg but no folded stack source")
	}
	if columns["folded_stacks"] {
		rows, err := tx.Query(`SELECT id, run_id, benchmark_name, folded_stacks, sampling_freq, created_at FROM flamegraphs`)
		if err != nil {
			return err
		}
		var flamegraphs []oldFlamegraph
		for rows.Next() {
			var fg oldFlamegraph
			if err := rows.Scan(&fg.id, &fg.runID, &fg.benchmarkName, &fg.foldedStacks, &fg.samplingFreq, &fg.createdAt); err != nil {
				_ = rows.Close()
				return err
			}
			flamegraphs = append(flamegraphs, fg)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if _, err := tx.Exec(`DROP TABLE flamegraphs`); err != nil {
			return err
		}
		if _, err := tx.Exec(`CREATE TABLE flamegraphs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			run_id INTEGER NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
			benchmark_name TEXT NOT NULL, folded_stacks_gz BLOB NOT NULL,
			sampling_freq INTEGER NOT NULL DEFAULT 997, created_at TEXT NOT NULL
		)`); err != nil {
			return err
		}
		for _, fg := range flamegraphs {
			compressed, err := gzipCompress([]byte(fg.foldedStacks))
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO flamegraphs(id, run_id, benchmark_name, folded_stacks_gz, sampling_freq, created_at) VALUES (?, ?, ?, ?, ?, ?)`, fg.id, fg.runID, fg.benchmarkName, compressed, fg.samplingFreq, fg.createdAt); err != nil {
				return err
			}
		}
	}
	_, err = tx.Exec(schemaSQL)
	return err
}

func migrateCompositeIdentity(tx *sql.Tx) error {
	if err := auditResultIdentity(tx); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_results_run_benchmark ON results(run_id, category, name)`); err != nil {
		return err
	}
	_, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_results_benchmark_run ON results(category, name, run_id)`)
	return err
}

func auditResultIdentity(tx *sql.Tx) error {
	var duplicateGroups, duplicateRows int
	err := tx.QueryRow(`SELECT COUNT(*), COALESCE(SUM(row_count - 1), 0) FROM (
		SELECT COUNT(*) AS row_count FROM results GROUP BY run_id, category, name HAVING COUNT(*) > 1
	)`).Scan(&duplicateGroups, &duplicateRows)
	if err != nil {
		return err
	}
	if duplicateGroups > 0 {
		return fmt.Errorf("cannot enforce benchmark identity: found %d duplicate groups containing %d extra rows", duplicateGroups, duplicateRows)
	}
	return nil
}

func migrateRegressionCache(tx *sql.Tx) error {
	if _, err := tx.Exec(`DROP TABLE IF EXISTS regression_cache`); err != nil {
		return err
	}
	_, err := tx.Exec(`CREATE TABLE regression_cache (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		run_id INTEGER NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
		branch TEXT NOT NULL, window INTEGER NOT NULL, min_points INTEGER NOT NULL,
		baseline_offset INTEGER NOT NULL, generation_key TEXT NOT NULL,
		response_json TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
		UNIQUE(run_id, branch, window, min_points, baseline_offset)
	);
	CREATE INDEX idx_regression_cache_branch_run ON regression_cache(branch, run_id DESC);
	CREATE INDEX idx_regression_cache_generation ON regression_cache(generation_key);`)
	return err
}

func migrateStoragePrecision(tx *sql.Tx) error {
	columns, err := tableColumns(tx, "results")
	if err != nil {
		return err
	}
	statements := []struct{ name, sql string }{
		{"sample_avg_variance_ns2", `ALTER TABLE results ADD COLUMN sample_avg_variance_ns2 REAL`},
		{"sample_data_version", `ALTER TABLE results ADD COLUMN sample_data_version INTEGER NOT NULL DEFAULT 0`},
		{"summary_version", `ALTER TABLE results ADD COLUMN summary_version INTEGER NOT NULL DEFAULT 1`},
	}
	for _, statement := range statements {
		if !columns[statement.name] {
			if _, err := tx.Exec(statement.sql); err != nil {
				return err
			}
		}
	}
	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS result_samples (
		result_id INTEGER NOT NULL REFERENCES results(id) ON DELETE CASCADE,
		sample_index INTEGER NOT NULL,
		avg_ns INTEGER NOT NULL CHECK (avg_ns > 0),
		PRIMARY KEY (result_id, sample_index)
	);
	DROP VIEW IF EXISTS results_with_run;
	CREATE VIEW results_with_run AS
	SELECT r.id AS result_id, r.category, r.name, r.min_ns, r.avg_ns, r.max_ns,
	       r.std_dev_ns, r.p50_ns, r.p95_ns, r.p99_ns, r.total_ns, r.iterations,
	       r.sample_count, r.sample_avg_variance_ns2, r.sample_data_version,
	       r.summary_version, ru.id AS run_id, ru.commit_hash, ru.commit_hash_full,
	       ru.commit_message, ru.commit_date, ru.branch, ru.run_date, ru.machine_id, ru.notes
	FROM results r JOIN runs ru ON r.run_id = ru.id`)
	return err
}

func tableExists(tx *sql.Tx, table string) (bool, error) {
	var count int
	err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count)
	return count > 0, err
}

func tableColumns(tx *sql.Tx, table string) (map[string]bool, error) {
	rows, err := tx.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	columns := make(map[string]bool)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}
