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
./bench trend "benchmark_name"
./bench backtest                         # Replay historical alerts with scorecards

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

## Development

See [AGENTS.md](AGENTS.md) for development.
