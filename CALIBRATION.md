# Phase 6 Calibration

The versioned `phase6-calibration-v1` replay was run in SQLite read-only mode against the repository's
`bench.db` through 2026-04-06 with:

```bash
go run ./cmd/bench --db bench.db calibrate --seed 1446 --output /tmp/phase6-calibration.json
```

For safety, `--output` must name a new file and cannot alias the database or its
SQLite sidecars.

Decision: **`uncalibrated_regression_score`**. No formal p-value or FDR guarantee
is claimed.

Input SHA-256: `44115f5e5a00b9407629a12d64f6dd91803ffbec45dadcb0465fe8eaa20b108b`.
Two verification replays produced the same JSON SHA-256:
`5970b8e669a8abd7dd878ac0e6e946950817cc97055668044d0415b8a0d40c23`.

Key evidence:

- New detector coverage was 29,374 of 30,848 hypotheses (95.2%), with 738 alerts
  (2.51% of eligible hypotheses). The legacy diagnostic covered 26,789 hypotheses
  (86.8%), with 1,222 alerts (4.56%). Legacy output is not used by production.
- No repeated unchanged-commit groups were available, so the required empirical
  null evidence is unavailable rather than inferred.
- The labeled stable-period proxy had a 2.33% false-alert rate. Its `[0, 0.01)`
  score bin contained 5.97% of values versus 1% nominal.
- 101 of 122 sufficiently long benchmark residual histories (82.8%) had material
  lag-1 autocorrelation (`|r| >= 0.30`); mean lag-1 correlation was 0.60.
- Deterministic residual-bootstrap detection was 4.0%, 19.4%, and 36.8% for
  sparse 2%, 5%, and 10% injections, and 5.3%, 24.1%, and 40.4% for broad
  injections at the same effects. The unchanged synthetic null false-alert rate
  was 0.57%.
- Machine ID and optimization mode had full coverage but no observed transitions.
  Zig toolchain version and harness version are not stored, so transition evidence
  for them is unavailable.

The JSON output contains the frozen configuration, input fingerprint,
nominal/empirical score bins and quantiles, versioned acceptance criteria, and
the fail-closed decision. These criteria were versioned with this implementation;
there is no prior committed preregistration, so the report does not claim one.
Re-running the command with identical persisted database content, branch, and
seed is byte-deterministic. SQLite may touch ephemeral `bench.db-shm` lock metadata
while reading WAL state; persisted database and WAL content remain unchanged.
