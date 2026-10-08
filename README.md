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
queued feature jobs until caught up, then polling for new work. Commits are
recorded in chronological order from the opentui `main` branch.

It runs on a Hetzner machine with minimal background processes to minimize
noise. Each run records multiple iterations to average out variability.

## Website authentication

GitHub sign-in lets allowed users create investigations, rerun pairs, and submit
candidate commits. Anyone can read benchmark results and investigation evidence.
Sign-in redirects to GitHub and then returns to the page where you started.

Create a [GitHub OAuth app](https://github.com/settings/developers). For the
production site, register this **Authorization callback URL** exactly:

```text
https://opentui-bench.fly.dev/api/auth/github/callback
```

Set these environment variables on the Go server at runtime:

| Variable                     | Value                                                                                                                           |
| ---------------------------- | ------------------------------------------------------------------------------------------------------------------------------- |
| `BENCH_GITHUB_CLIENT_ID`     | The OAuth app's client ID.                                                                                                      |
| `BENCH_GITHUB_CLIENT_SECRET` | The OAuth app's client secret.                                                                                                  |
| `BENCH_GITHUB_ALLOWED_USERS` | Comma-separated GitHub logins, such as `simon,teammate`. Matching ignores case.                                                 |
| `BENCH_GITHUB_CALLBACK_URL`  | The exact absolute callback URL registered with GitHub. Its origin must match the website's origin.                             |
| `BENCH_SESSION_SECRET`       | A random secret of at least 32 bytes. Generate one with `openssl rand -hex 32` and keep it stable across restarts and replicas. |
| `BENCH_API_KEY`              | A nonempty bearer token for the benchmark worker and CLI. Required whenever GitHub sign-in is configured.                       |

For another deployment, replace the hostname in the callback URL. HTTPS is
required in production. To test OAuth with the Vite development server, register
`http://localhost:3000/api/auth/github/callback` in a separate OAuth app and use
that value for `BENCH_GITHUB_CALLBACK_URL`. Vite proxies `/api` to the Go
server. Do not put these secrets in frontend environment variables or browser
storage.

The browser uses a signed, HttpOnly session cookie that expires after eight
hours. Production cookies require HTTPS. Each write checks the session's GitHub
login against the allowed-user list and checks the request's `Origin` against
the configured callback origin. Sign out clears the session cookie. Changing
`BENCH_SESSION_SECRET` invalidates all existing sessions.

The authentication endpoints are `GET /api/auth/session`,
`GET /api/auth/github`, `GET /api/auth/github/callback`, and
`POST /api/auth/logout`. The session endpoint reports whether GitHub sign-in is
configured and whether the browser is signed in. GitHub access tokens stay on
the server. Browser sessions authorize only investigation writes; recording,
artifact uploads, job claims, and generic job creation require the worker bearer
token. The website has no API-key entry control.

If you set any GitHub authentication variable or `BENCH_SESSION_SECRET`, you
must set all variables in the table. A server configured only with
`BENCH_API_KEY` still starts, but website writes are read-only and the sign-in
control reports that GitHub sign-in is unavailable. With all authentication
variables unset, local development allows writes without sign-in.

## Database

Data is stored in a SQLite database. You can download it via the "Export" link
in the web UI sidebar, or directly at `/api/database/download`.

Aggregate benchmark history is retained indefinitely. Bulky CPU profiles are
limited to 50 recent runs, including runs with partial captures, and 128 MiB by
default, whichever limit is reached first. Generated SVGs use a five-run
filesystem cache instead of SQLite. Override the server limits with
`PROFILE_RETENTION_RUNS`, `PROFILE_RETENTION_MIB`, and `SVG_CACHE_MAX_RUNS`.

JavaScript run recording and automatic scheduling remain disabled until the
benchmark protocol is qualified. Set `BENCH_ENABLE_JAVASCRIPT_RUNS=1` on the
server only after qualification to advertise the capability to workers.

Remote job claims use the independently advertised `job_lease_protocol`
capability. During rollout, incompatible server and worker versions refuse to
claim work, so the Fly server and Hetzner worker can be upgraded in either
order. Workers generate the lease token before claiming, allowing a lost claim
response to be retried without consuming another job. The server stores only its
SHA-256 digest; jobs already running during the v7 migration remain leased until
the normal 24-hour stale recovery window expires.

Profile pruning makes deleted pages reusable, bounding steady-state growth, but
does not immediately shrink the SQLite file. Database downloads are compact
snapshots and do not transfer those free pages. Prune an existing source
database and optionally return its free pages to the filesystem:

```bash
./bench --db /path/to/bench.db prune --compact
```

## Detector Calibration

`bench calibrate` opens the selected database in SQLite read-only mode and runs
the versioned Phase 6 replay in strict `(run_date, id)` order. Detector
parameters are frozen; only the branch, deterministic injection seed, and JSON
output path are flags. The output path must be a new file and cannot alias the
database or its sidecars. The report labels the old median/SEM detector as a
legacy diagnostic and never uses it for production alerts. WAL reads may touch
ephemeral `-shm` lock metadata, but do not write persisted database or WAL
content. Until every versioned criterion has adequate evidence and passes, API
and UI results remain `uncalibrated_regression_score` with no formal p-value or
FDR guarantee.

## Development

See [AGENTS.md](AGENTS.md) for development.
