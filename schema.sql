-- OpenTUI Benchmark Tracker Schema
-- SQLite database for tracking Zig benchmark performance over time

-- A single benchmark run (one invocation of `zig build bench`)
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

-- Individual benchmark results within a run
-- When sample_count > 1, statistics are computed from multiple benchmark invocations
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

-- Memory statistics (optional, per-result)
CREATE TABLE IF NOT EXISTS mem_stats (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    result_id INTEGER NOT NULL REFERENCES results(id) ON DELETE CASCADE,
    stat_name TEXT NOT NULL,
    bytes INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_mem_stats_result ON mem_stats(result_id);

-- View for easy querying with run context
CREATE VIEW IF NOT EXISTS results_with_run AS
SELECT 
    r.id as result_id,
    r.category,
    r.name,
    r.min_ns,
    r.avg_ns,
    r.max_ns,
    r.std_dev_ns,
    r.p50_ns,
    r.p95_ns,
    r.p99_ns,
    r.total_ns,
    r.iterations,
    r.sample_count,
    ru.id as run_id,
    ru.commit_hash,
    ru.commit_hash_full,
    ru.commit_message,
    ru.commit_date,
    ru.branch,
    ru.run_date,
    ru.machine_id,
    ru.notes
FROM results r
JOIN runs ru ON r.run_id = ru.id;
