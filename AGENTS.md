# AGENTS.md

AI coding help for working with this repository.

**opentui-bench** tracks Zig benchmark performance over time for the [opentui
project](https://github.com/anomalyco/opentui).

- Go web server with SQLite database
- CLI for recording/viewing benchmark results
- Solid.js web UI for visualizing trends

## Development Commands

```bash
make dev             # Start development server (auto-reloads, logs to dev.log)
make display-log     # Display the last 100 lines of dev.log

## Other commands:
make build           # Build the binary (not required for dev)
make test            # Run all tests
make serve           # Run the server directly (not required for dev)
make help            # Show all available commands
```

**IMPORTANT:**

- `make dev` is all you need - it auto-rebuilds on file changes.
- Server logs to `dev.log`. Use `make display-log` to read it.
- NEVER stop the server! It auto-reloads on changes.
- Use **localhost:3000** for frontend development (HMR, instant updates).
- Port 8080 serves embedded static files and only updates after `make build`.

## CLI Commands

```bash
./bench list                         # List recorded runs
./bench show <commit>                # Show run details
./bench compare <commit1> <commit2>  # Compare two runs
./bench trend "benchmark_name"       # Show performance trend
```

## Architecture

- `cmd/bench/` - CLI entry point
- `internal/db/` - SQLite database layer
- `internal/web/` - HTTP handlers and embedded static files
- `internal/runner/` - Benchmark execution and recording
- `internal/record/` - Benchmark result parser and data structures
