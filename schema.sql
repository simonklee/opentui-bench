-- OpenTUI Benchmark Tracker Schema
-- SQLite database for tracking Zig benchmark performance over time

-- A benchmark run aggregating one or more benchmark process invocations
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

-- Memory statistics (optional, per-result)
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
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    status TEXT NOT NULL DEFAULT 'pending',
    kind TEXT NOT NULL DEFAULT 'benchmark',
    branch TEXT NOT NULL,
    commit_hash TEXT,
    repo_url TEXT NOT NULL DEFAULT 'origin',
    samples INTEGER NOT NULL DEFAULT 3,
    profile TEXT NOT NULL DEFAULT 'cpu',
    notes TEXT,
    created_at TEXT NOT NULL,
    started_at TEXT,
    completed_at TEXT,
    error TEXT,
    run_id INTEGER REFERENCES runs(id),
    requested_by TEXT,
    claim_token TEXT, -- SHA-256 digest; the raw bearer token is held only by its claimant
    legacy_tokenless INTEGER NOT NULL DEFAULT 0,
    benchmark_kind TEXT NOT NULL DEFAULT 'zig',
    benchmark_suite TEXT NOT NULL DEFAULT 'core-default',
    protocol_version INTEGER NOT NULL DEFAULT 1,
    manifest_hash TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status);
CREATE INDEX IF NOT EXISTS idx_jobs_created ON jobs(created_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_jobs_claim_token ON jobs(claim_token) WHERE claim_token IS NOT NULL;

-- Cached regression snapshots for history rendering
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
    r.sample_avg_variance_ns2,
    r.sample_data_version,
    r.summary_version,
    ru.id as run_id,
    ru.commit_hash,
    ru.commit_hash_full,
    ru.commit_message,
    ru.commit_date,
    ru.branch,
    ru.run_date,
    ru.machine_id,
    ru.notes,
    ru.zig_optimize,
    ru.benchmark_kind,
    ru.benchmark_suite,
    ru.protocol_version,
    ru.bun_version,
    ru.zig_version,
    ru.manifest_hash,
    ru.manifest_json
FROM results r
JOIN runs ru ON r.run_id = ru.id;

PRAGMA user_version = 7;
