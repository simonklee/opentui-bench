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

GitHub Actions triggers benchmarks every 30 minutes, processing one commit at a
time in chronological order from the opentui `main` branch.

It runs on a Hetzner machine with minimal background processes to minimize
noise. Each run records multiple iterations to average out variability.

## Database

Data is stored in a SQLite database but is currently not versioned or available
for download. I will add ability to get the raw database in future.

## Development

See [AGENTS.md](AGENTS.md) for development.
