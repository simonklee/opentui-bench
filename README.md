# OpenTUI Bench

This project stores benchmark history, provides comparison tools, and tracks
performance over time.

## Quick Start

```bash
# Build
make build

# Record a benchmark run
./bench record --repo ~/insmo.com/opentui

# View results
./bench list
./bench show <commit>
./bench compare <commit1> <commit2>
./bench trend <result_id>
./bench backtest                         # Replay historical alerts with scorecards
./bench calibrate --output report.json   # Frozen chronological calibration replay

# Start web UI
make serve
```

## Recording Options

```bash
./bench record --repo /path/to/opentui --notes "After optimization"  # Add notes
./bench record --repo /path/to/opentui --filter "UTF-8"              # Filter benchmark category
./bench record --repo /path/to/opentui --optimize Debug              # Different optimization level
```

## Continuous benchmarking

The Hetzner benchmark worker runs continuously, processing main commits and
queued feature jobs until caught up, then polling for new work.
Commits are recorded in chronological order from the opentui `main` branch.

It runs on a Hetzner machine with minimal background processes to minimize
noise. Each run records multiple iterations to average out variability.

## Database

Data is stored in a SQLite database. You can download it via the "Export" link
in the web UI sidebar, or directly at `/api/database/download`.

Aggregate benchmark history is retained indefinitely. Bulky CPU profiles are
limited to 256 complete recent runs and 256 MiB by default, whichever limit is
reached first; generated SVGs use a five-run filesystem cache instead of SQLite.
Override the server limits with `PROFILE_RETENTION_RUNS`,
`PROFILE_RETENTION_MIB`, and `SVG_CACHE_MAX_RUNS`.

JavaScript run recording and automatic scheduling remain disabled until the
benchmark protocol is qualified. Set `BENCH_ENABLE_JAVASCRIPT_RUNS=1` on the
server only after qualification to advertise the capability to workers.

Remote job claims use the independently advertised `job_lease_protocol`
capability. During rollout, incompatible server and worker versions refuse to
claim work, so the Fly server and Hetzner worker can be upgraded in either order.
Workers generate the lease token before claiming, allowing a lost claim response
to be retried without consuming another job. The server stores only its SHA-256
digest; jobs already running during the v7 migration remain leased until the
normal 24-hour stale recovery window expires.

Prune an existing database and optionally return free pages to the filesystem:

```bash
./bench --db /path/to/bench.db prune --compact
```

## Detector Calibration

`bench calibrate` opens the selected database in SQLite read-only mode and runs the versioned
Phase 6 replay in strict `(run_date, id)` order. Detector parameters are frozen;
only the branch, deterministic injection seed, and JSON output path are flags.
The output path must be a new file and cannot alias the database or its sidecars.
The report labels the old median/SEM detector as a legacy diagnostic and never
uses it for production alerts. WAL reads may touch ephemeral `-shm` lock metadata,
but do not write persisted database or WAL content. Until every versioned criterion has adequate
evidence and passes, API and UI results remain `uncalibrated_regression_score`
with no formal p-value or FDR guarantee.

## Development

See [AGENTS.md](AGENTS.md) for development.
