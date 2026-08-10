## Objective

- Expand `core-default` from four to seven representative JavaScript benchmarks, while simplifying instrumentation so tests establish correctness and benchmarks primarily measure work.
- Requalify the suite, update its manifest/tracker identity, then perform a coordinated production rollout after the OpenTUI merge.

## Important Details

- Initial OpenTUI benchmark work was squashed and pushed as `0197437c3d4dc851b71d829230da4cd4ffde09fc` on `origin/ot-js-benchmarks`.
- New branch `ot-js-benchmarks-2` has local commit `4e34c641c46c65caf7bf5426d383f544100002af`.
- Tracker has local commit `5543ffd` pinning the seven-case manifest.
- Production still accepts the deployed four-case manifest `sha256:0fa487783682b1227bfd4bf735fe1a969ea03f045bb8a68f87c1e41174cb3794`.
- Current seven-case candidate manifest is `sha256:9f49f938973c8689c06166b6823cdeb96f1d1a224913565e5847969678f52d00`; simplification will change it again.
- Added candidates:
- `JS Text Table / proportional-column-widths`
- `JS Text / text-buffer-word-wrap-measure`
- `JS Buffer / draw-box-titled-scissored`
- Final seven-case qualification completed in 45.9 seconds; worst inner RSD was 33,148 ppm.
- User accepts benchmark-level work signals without exact validation of every intermediate `drawBox`; correctness belongs in tests.
- Proposed simplifications are not yet applied:
- Make `leaf-width-calculate` actually time mutation plus `calculateLayout()`; bump `workload_version` to `2`.
- Reduce allocator and text-measurement checks inside timed operations.
- Remove redundant post-batch Yoga fixture validation and parser decoding.
- Reduce canonical `drawBox` validation to operation count plus cumulative title-cell signal.
- `bench:box-draw` remains useful for standalone exploratory/profiling measurements.
- Production reports `{"javascript_runs":1,"job_lease_protocol":2}` and still runs tracker `6986ac8`.
- Exact old-commit rerun: JS job `14` failed frozen install at `212e88e6`; Zig job `15` completed as run `488`.
- `/home/simon/src/opendocs/bench/ts-benchmark.md` remains modified and uncommitted.

## Work State

### Completed

- Qualified and deployed the initial four-case production pipeline.
- Recorded successful initial JS job `12` / run `486` and Zig job `13` / run `487`.
- Added and tested the three new candidate workloads.
- Committed expanded OpenTUI suite as `4e34c641`.
- Updated tracker digest, scheduler pin, and seven-case API fixture in `5543ffd`.
- `make test`, `make build`, frozen Bun install, benchmark tests, and formatting checks pass.
- Updated the rollout plan with the deployed/candidate identity split and coordinated rollout sequence.

### Active

- Simplifying benchmark instrumentation according to “tests test; benchmarks measure.”
- Preparing to requalify all seven cases and regenerate the canonical manifest after those changes.

### Blocked

- Production rollout must wait for the final simplified suite, recomputed digest, pushed commits, and the relevant OpenTUI `main` merge.
- No technical blocker.

## Relevant Files

- `/home/simon/src/wt/ot-js-benchmarks/packages/core/src/benchmark/js-benchmark-cases.ts`: seven canonical workloads and current validation overhead.
- `/home/simon/src/wt/ot-js-benchmarks/packages/core/src/benchmark/js-benchmark-harness.test.ts`: workload identity, lifecycle, and corruption tests to simplify.
- `/home/simon/src/wt/ot-js-benchmarks/packages/core/src/benchmark/box-draw-benchmark.ts`: existing standalone lighter-validation drawing suite.
- `/home/simon/src/opentui-bench/internal/jsbench/protocol.go`: canonical tracker manifest digest.
- `/home/simon/src/opentui-bench/internal/web/javascript_api_test.go`: ordered canonical API fixture.
- `/home/simon/src/opentui-bench/scripts/run-benchmarks.sh`: worker scheduler manifest pin and automatic-main cursor logic.
- `/home/simon/src/opendocs/bench/ts-benchmark.md`: rollout state, suite contract, and coordinated deployment checklist.

## Your task

Great, I committed the changes, looks good. =Let's do another background task
where we test things intern locally and that's the web site and the website
experience. I've downloaded the BenchDB and you can run the local Go and the
frontend server and use the Playwriter skill to an MCP to access the web page.
