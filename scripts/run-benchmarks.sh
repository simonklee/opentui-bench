#!/usr/bin/env bash
# shellcheck disable=SC2034
#
# run-benchmarks.sh - Orchestrate benchmark runs for opentui
#
# Usage: run-benchmarks.sh [options]
#
# This script manages the benchmarking process:
# 1. Sets up repositories (opentui-bench, opentui)
# 2. Identifies commits to benchmark
# 3. Runs benchmarks using 'bench record --api-url'
# 4. Results are posted directly to the Fly.io API
#
# It is robust, uses locking to prevent concurrent runs, and handles errors gracefully.

# Enable xtrace (debug tracing) if DEBUG environment variable is set.
if [[ ${DEBUG:-} =~ ^1|yes|true$ ]]; then
	set -o xtrace
fi

# Strict mode
if ! (return 0 2>/dev/null); then
	set -o errexit  # Exit on error
	set -o nounset  # Error on undefined variables
	set -o pipefail # Pipeline fails if any command fails
fi

# Enable errtrace
set -o errtrace

# Constants
readonly SCRIPT_NAME="${0##*/}"
SCRIPT_PATH="${BASH_SOURCE[0]}"
while [[ -L "$SCRIPT_PATH" ]]; do
	SCRIPT_DIR="$(cd "$(dirname "$SCRIPT_PATH")" && pwd)"
	SCRIPT_PATH="$(readlink "$SCRIPT_PATH")"
	[[ "$SCRIPT_PATH" != /* ]] && SCRIPT_PATH="$SCRIPT_DIR/$SCRIPT_PATH"
done
SCRIPT_DIR="$(cd "$(dirname "$SCRIPT_PATH")" && pwd)"
readonly SCRIPT_DIR

# Configuration Constants
readonly REPOS_DIR="$HOME/repos"
readonly BENCH_REPO="$REPOS_DIR/opentui-bench"
readonly OPENTUI_REPO="$REPOS_DIR/opentui"
readonly LOG_FILE="$HOME/benchmark.log"
readonly ALERT_STATE_FILE="$HOME/.cache/opentui-bench/last-alert"
readonly JS_BENCHMARK_SUITE="core-default"
readonly JS_PROTOCOL_VERSION=1
# The canonical JS manifest hash is advertised by the API via /api/capabilities
# so the worker never needs a script update when the benchmark workload changes.
readonly JS_BUN_VERSION="1.4.0"
readonly JS_NODE_VERSION="26.7.0"

# API configuration - set these as environment variables
: "${API_URL:=https://opentui-bench.fly.dev}"
: "${API_KEY:?BENCH_API_KEY or API_KEY must be set}"
: "${POLL_INTERVAL:=60}"
: "${ALERT_COOLDOWN:=3600}"

# Export PATH to include necessary binaries
export PATH="$HOME/.bun/bin:$HOME/.local/node-v$JS_NODE_VERSION/bin:$HOME/.cargo/bin:$HOME/anyzig:$HOME/.fly/bin:/usr/local/go/bin:$PATH"

# Global variables
USE_COLOR=true
verbose=false
dry_run=false
cron_mode=false
daemon_mode=false
COMMAND=""
did_work=false

init_colors() {
	if [[ -t 1 ]] && [[ -n "${TERM:-}" ]] && [[ "${TERM:-}" != "dumb" ]] && [[ -z "${NO_COLOR:-}" ]] && $USE_COLOR; then
		RED=$(tput setaf 1 2>/dev/null || printf '\033[0;31m')
		GREEN=$(tput setaf 2 2>/dev/null || printf '\033[0;32m')
		YELLOW=$(tput setaf 3 2>/dev/null || printf '\033[1;33m')
		CYAN=$(tput setaf 6 2>/dev/null || printf '\033[0;36m')
		BOLD=$(tput bold 2>/dev/null || printf '\033[1m')
		DIM=$(tput dim 2>/dev/null || printf '\033[2m')
		NC=$(tput sgr0 2>/dev/null || printf '\033[0m')
	else
		RED='' GREEN='' YELLOW='' CYAN='' BOLD='' DIM='' NC=''
	fi
}
init_colors

# log writes to stdout and append to log file
log() {
	local msg
	msg="[$(date -Iseconds)] $*"
	echo -e "$msg" | tee -a "$LOG_FILE"
}

# err writes to stderr and append to log file
err() {
	local msg
	msg="[$(date -Iseconds)] ERROR: $*"
	echo -e "${RED}$msg${NC}" >&2
	echo "$msg" >>"$LOG_FILE"
}

alert() {
	local message="$*"
	local now last_alert=0
	now=$(date +%s)
	mkdir -p "${ALERT_STATE_FILE%/*}"
	if [[ -f "$ALERT_STATE_FILE" ]]; then
		read -r last_alert <"$ALERT_STATE_FILE" || last_alert=0
	fi

	# A restarting service should not page repeatedly for the same outage.
	if ((now - last_alert < ALERT_COOLDOWN)); then
		err "Alert suppressed by ${ALERT_COOLDOWN}s cooldown: $message"
		return 0
	fi

	printf '%s\n' "$now" >"$ALERT_STATE_FILE"
	err "ALERT: $message"
	if command -v systemd-cat &>/dev/null; then
		printf '%s\n' "$message" | systemd-cat --identifier=opentui-bench-worker --priority=crit || true
	fi
	if [[ -n "${ALERT_WEBHOOK_URL:-}" ]]; then
		local payload
		payload=$(jq -cn --arg text "$message" '{text: $text}')
		curl --fail --silent --show-error --max-time 15 \
			-H 'Content-Type: application/json' --data "$payload" "$ALERT_WEBHOOK_URL" >/dev/null || \
			err "Failed to deliver alert webhook"
	fi
}

info() {
	if $verbose; then
		echo -e "${DIM}$*${NC}"
	fi
}

declare -a CLEANUP_TASKS=()

cleanup_register() {
	CLEANUP_TASKS+=("$1")
}

trap_exit() {
	local exit_code=$?
	local i
	for ((i = ${#CLEANUP_TASKS[@]} - 1; i >= 0; i--)); do
		eval "${CLEANUP_TASKS[i]}" || true
	done

	if ((exit_code != 0)); then
		err "Script failed with exit code $exit_code"
		alert "OpenTUI benchmark worker exited with status $exit_code on $(hostname)"
	fi
	exit "$exit_code"
}

trap_err() {
	local exit_code=$?
	((exit_code != 0)) || return 0
	err "Command failed with exit code $exit_code"
	local frame=0
	while caller "$frame" >/dev/null 2>&1; do
		local line func file
		read -r line func file <<<"$(caller "$frame")"
		if ((frame == 0)); then
			err "  at $func ($file:$line)"
		else
			err "  called from $func ($file:$line)"
		fi
		((frame++))
	done
}

if ! (return 0 2>/dev/null); then
	trap trap_exit EXIT
	trap trap_err ERR
	trap 'exit 0' INT TERM
fi

SCRIPT_LOCK=""
lock_acquire() {
	local lock_dir="/tmp/${SCRIPT_NAME}.${UID}.lock"
	if mkdir "$lock_dir" 2>/dev/null; then
		SCRIPT_LOCK="$lock_dir"
		cleanup_register "lock_release"
		info "Acquired script lock: $lock_dir"
	else
		err "Script is already running (lock exists: $lock_dir)"
		exit 1
	fi
}

lock_release() {
	if [[ -n "${SCRIPT_LOCK:-}" && -d "$SCRIPT_LOCK" ]]; then
		rmdir "$SCRIPT_LOCK" 2>/dev/null || true
		info "Released script lock: $SCRIPT_LOCK"
	fi
}

check_dependencies() {
	local missing=()
	for cmd in git curl jq go; do
		if ! command -v "$cmd" &>/dev/null; then
			missing+=("$cmd")
		fi
	done
	if [[ ${#missing[@]} -gt 0 ]]; then
		err "Missing required commands: ${missing[*]}"
		return 1
	fi
	return 0
}

has_stable_javascript_bun() {
	local bun_revision bun_version
	if ! command -v bun &>/dev/null; then
		info "Bun is unavailable; JavaScript benchmark work is disabled"
		return 1
	fi
	if ! bun_revision=$(timeout --kill-after=1s 5s bun --revision 2>/dev/null); then
		info "Unable to read Bun revision; JavaScript benchmark work is disabled"
		return 1
	fi
	bun_version=${bun_revision%%+*}
	if [[ "$bun_revision" != *+* || "$bun_version" != "$JS_BUN_VERSION" ]]; then
		info "Bun must be stable $JS_BUN_VERSION for JavaScript benchmarks (found ${bun_revision:-unavailable})"
		return 1
	fi
	return 0
}

has_stable_javascript_node() {
	local node_version
	if ! command -v node &>/dev/null; then
		info "Node is unavailable; Node benchmark work is disabled"
		return 1
	fi
	if ! node_version=$(timeout --kill-after=1s 5s node --version 2>/dev/null); then
		info "Unable to read Node version; Node benchmark work is disabled"
		return 1
	fi
	if [[ "$node_version" != "v$JS_NODE_VERSION" ]]; then
		info "Node must be $JS_NODE_VERSION for JavaScript benchmarks (found ${node_version:-unavailable})"
		return 1
	fi
	return 0
}

setup_repos() {
	log "Setting up repositories..."
	mkdir -p "$REPOS_DIR"

	# opentui-bench
	if [[ ! -d "$BENCH_REPO" ]]; then
		log "Cloning opentui-bench repo"
		git clone git@github.com:simonklee/opentui-bench.git "$BENCH_REPO"
	fi

	log "Updating opentui-bench..."
	cd "$BENCH_REPO"
	git fetch origin
	git reset --hard origin/main
	local installed_script="$SCRIPT_DIR/${SCRIPT_PATH##*/}"
	if ! cmp -s "$BENCH_REPO/scripts/run-benchmarks.sh" "$installed_script"; then
		log "Updating installed benchmark worker..."
		local worker_script_tmp
		worker_script_tmp=$(mktemp "$SCRIPT_DIR/.run-benchmarks.sh.XXXXXX")
		install -m 755 "$BENCH_REPO/scripts/run-benchmarks.sh" "$worker_script_tmp"
		mv -f "$worker_script_tmp" "$installed_script"
		lock_release
		exec "$installed_script" "$@"
	fi
	make backend-build

	# opentui
	if [[ ! -d "$OPENTUI_REPO" ]]; then
		log "Cloning opentui repo"
		git clone git@github.com:anomalyco/opentui.git "$OPENTUI_REPO"
	fi

	log "Updating opentui..."
	cd "$OPENTUI_REPO"
	if ! git remote | grep -q "^simonklee$"; then
		log "Adding simonklee remote"
		git remote add simonklee git@github.com:simonklee/opentui.git
	fi
	git fetch origin
	git fetch simonklee
}

reset_opentui() {
	if [[ -d "$OPENTUI_REPO" ]]; then
		cd "$OPENTUI_REPO"
		git reset --hard HEAD 2>/dev/null || true
	fi
}

run_benchmarks() {
	cd "$OPENTUI_REPO"
	git fetch origin

	# Ask the API for the latest recorded commit
	local latest_response latest_recorded
	latest_response=$(curl --fail --silent --show-error --max-time 120 \
		"$API_URL/api/latest-commit?branch=main")
	latest_recorded=$(jq -er 'if has("commit_hash_full") then (.commit_hash_full // "") else error("missing commit_hash_full") end' \
		<<<"$latest_response")

	local commits
	if [[ -n "$latest_recorded" ]] && git cat-file -e "$latest_recorded" 2>/dev/null; then
		log "Latest recorded: ${latest_recorded:0:7}"
		commits=$(git log --reverse --format='%H' "${latest_recorded}..origin/main")
	else
		log "No recorded commits found, checking last commit on main"
		commits=$(git log --format='%H' origin/main -1)
	fi

	local next_commit="${commits%%$'\n'*}"
	if [[ -z "$next_commit" ]]; then
		log "All commits already recorded, nothing to do"
		return 0
	fi
	did_work=true

	log "Processing commit: ${next_commit:0:7}"

	# Register cleanup to reset repo if we fail during processing
	cleanup_register "reset_opentui"

	# Checkout the commit
	git checkout "$next_commit"

	# Record posts results directly to the API
	cd "$BENCH_REPO"
	if $dry_run; then
		log "Dry run: would exec ./bench record --api-url ..."
	else
		local record_output record_status
		set +o errexit
		record_output=$(./bench record --repo "$OPENTUI_REPO" \
			--api-url "$API_URL" --api-key "$API_KEY" \
			--samples 3 --profile cpu --notes "Hetzner CCX13" \
			--branch main 2>&1)
		record_status=$?
		set -o errexit
		printf '%s\n' "$record_output" | tee -a "$LOG_FILE"
		if ((record_status != 0)); then
			if [[ "$record_output" == *"database or disk is full"* ]]; then
				alert "OpenTUI benchmark storage is full; commit ${next_commit:0:7} could not be recorded"
			elif [[ "$record_output" == *"post results to API"* || "$record_output" == *"artifact upload errors"* ]]; then
				alert "OpenTUI benchmark API rejected results for commit ${next_commit:0:7}"
			fi
			return "$record_status"
		fi
	fi

	# Reset opentui repo
	reset_opentui

	log "Benchmark run complete for ${next_commit:0:7}"
}

schedule_javascript_main_job() {
	local runtime=$1 runtime_version script
	case "$runtime" in
	bun) runtime_version=$JS_BUN_VERSION; script=bench:js ;;
	node) runtime_version=$JS_NODE_VERSION; script=bench:js:node ;;
	*) err "Unsupported JavaScript runtime: $runtime"; return 1 ;;
	esac
	cd "$OPENTUI_REPO"
	if ! has_stable_javascript_bun; then
		return 0
	fi
	if [[ "$runtime" == node ]] && ! has_stable_javascript_node; then
		return 0
	fi
	local capabilities manifest_hash
	if ! capabilities=$(curl --fail --silent --show-error --max-time 120 "$API_URL/api/capabilities") ||
		! jq -e --arg runtime "$runtime" \
			'.javascript_runs == 1 and .job_lease_protocol == 3 and (.javascript_runtimes | index($runtime) != null)' \
			<<<"$capabilities" >/dev/null ||
		! manifest_hash=$(jq -er '.javascript_manifest_hash // empty | select(startswith("sha256:"))' <<<"$capabilities"); then
		info "Server does not advertise $runtime JavaScript scheduling support; skipping automatic scheduling"
		return 0
	fi

	local automatic_jobs
	automatic_jobs=$(curl --fail --silent --show-error --max-time 120 --get "$API_URL/api/jobs" \
		--data-urlencode 'branch=main' --data-urlencode 'limit=1000000' \
		--data-urlencode 'requested_by=automatic' --data-urlencode 'benchmark_kind=js' \
		--data-urlencode "benchmark_suite=$JS_BENCHMARK_SUITE" \
		--data-urlencode "protocol_version=$JS_PROTOCOL_VERSION" \
		--data-urlencode "manifest_hash=$manifest_hash" \
		--data-urlencode "js_runtime=$runtime" \
		--data-urlencode "runtime_version=$runtime_version")

	# Do not queue ahead of an existing attempt or create a duplicate for it.
	if jq -e 'any(.[]; .status != "completed" and .status != "failed" and .status != "cancelled")' <<<"$automatic_jobs" >/dev/null; then
		info "Automatic $runtime JavaScript benchmark job is already outstanding"
		return 0
	fi

	local terminal_commits latest_attempt="" next_commit=""
	terminal_commits=$(jq -r '.[] | select(.status == "completed" or .status == "failed" or .status == "cancelled") | .commit_hash | select(length > 0)' \
		<<<"$automatic_jobs")
	if [[ -n "$terminal_commits" ]]; then
		for commit in $(git rev-list origin/main); do
			if grep -Fxq "$commit" <<<"$terminal_commits"; then
				latest_attempt="$commit"
				break
			fi
		done
	fi

	if [[ -n "$latest_attempt" ]]; then
		for commit in $(git rev-list --reverse "${latest_attempt}..origin/main"); do
			if javascript_harness_exists "$commit" "$script"; then
				next_commit=$commit
				break
			fi
		done
	else
		for commit in $(git log --reverse --format='%H' origin/main -- packages/core/package.json \
			packages/core/src/benchmark/js-benchmark.ts packages/core/src/benchmark/js-benchmark-harness.ts); do
			if javascript_harness_exists "$commit" "$script"; then
				next_commit="$commit"
				break
			fi
		done
	fi
	if [[ -z "$next_commit" ]]; then
		info "JavaScript main history is caught up or has no canonical harness"
		return 0
	fi

	did_work=true
	if $dry_run; then
		log "Dry run: would queue automatic $runtime JavaScript benchmark for ${next_commit:0:7}"
		return 0
	fi

	local payload
	payload=$(jq -cn \
		--arg branch main \
		--arg commit_hash "$next_commit" \
		--arg suite "$JS_BENCHMARK_SUITE" \
		--arg manifest "$manifest_hash" \
		--arg runtime "$runtime" \
		--arg runtime_version "$runtime_version" \
		--argjson protocol "$JS_PROTOCOL_VERSION" '
		{
			branch: $branch,
			commit_hash: $commit_hash,
			benchmark_kind: "js",
			benchmark_suite: $suite,
			protocol_version: $protocol,
			manifest_hash: $manifest,
			js_runtime: $runtime,
			runtime_version: $runtime_version,
			samples: 3,
			profile: "none",
			requested_by: "automatic"
		}')
	curl --fail --silent --show-error --max-time 120 \
		-X POST "$API_URL/api/jobs" \
		-H "Authorization: Bearer $API_KEY" \
		-H 'Content-Type: application/json' \
		--data "$payload" >/dev/null
	log "Queued automatic $runtime JavaScript benchmark for ${next_commit:0:7}"
}

javascript_harness_exists() {
	local commit=$1 script=$2
	git cat-file -e "${commit}:packages/core/src/benchmark/js-benchmark.ts" 2>/dev/null &&
		git cat-file -e "${commit}:packages/core/src/benchmark/js-benchmark-harness.ts" 2>/dev/null &&
		git show "${commit}:packages/core/package.json" 2>/dev/null |
		jq -e --arg script "$script" '.scripts[$script] | type == "string" and length > 0' >/dev/null
}

run_queued_jobs() {
	log "Checking for queued jobs..."
	cd "$BENCH_REPO"

	# Process one queued job between main commits.
	if $dry_run; then
		log "Dry run: would exec ./bench worker --once ..."
		return 0
	fi

	local -a worker_args=(--repo "$OPENTUI_REPO" --api-url "$API_URL" --api-key "$API_KEY" --once)
	if ! has_stable_javascript_bun; then
		# Still recover stale jobs through the claim endpoint, but never claim JS.
		worker_args+=(--kind zig)
	fi

	local worker_output worker_status
	set +o errexit
	worker_output=$(./bench worker "${worker_args[@]}" 2>&1)
	worker_status=$?
	set -o errexit
	printf '%s\n' "$worker_output" | tee -a "$LOG_FILE"
	if [[ "$worker_output" != *"No pending jobs"* ]]; then
		did_work=true
	fi
	if [[ "$worker_output" == *" failed: "* ]] && ((worker_status == 0)); then
		worker_status=1
	fi
	if ((worker_status != 0)); then
		if [[ "$worker_output" == *"database or disk is full"* ]]; then
			alert "OpenTUI benchmark storage is full; queued job results could not be recorded"
		elif [[ "$worker_output" == *"post results to API"* || "$worker_output" == *"artifact upload errors"* ]]; then
			alert "OpenTUI benchmark API rejected queued job results"
		else
			alert "OpenTUI queued benchmark job failed on $(hostname)"
		fi
		return "$worker_status"
	fi

	return 0
}

# --- Main ---

show_usage() {
	cat <<EOF
Usage: ${SCRIPT_NAME} [options]

Options:
    -v, --verbose    Enable verbose output
    -n, --dry-run    Show what would be done without doing it
    -d, --daemon     Keep processing work; sleep only when caught up
    -h, --help       Show this help message

Environment variables:
    API_URL          API endpoint (default: https://opentui-bench.fly.dev)
    API_KEY          API key for authentication (required)
    POLL_INTERVAL    Seconds to sleep when caught up (default: 60)
    ALERT_WEBHOOK_URL  Optional JSON webhook receiving {"text":"..."}
EOF
}

parse_args() {
	while [[ $# -gt 0 ]]; do
		case "$1" in
		-v | --verbose)
			verbose=true
			shift
			;;
		-n | --dry-run)
			dry_run=true
			shift
			;;
		-d | --daemon)
			daemon_mode=true
			shift
			;;
		-h | --help)
			show_usage
			exit 0
			;;
		*)
			err "Unknown argument: $1"
			show_usage
			exit 1
			;;
		esac
	done
}

main() {
	parse_args "$@"

	lock_acquire

	log "Starting benchmark run"
	check_dependencies

	setup_repos "$@"
	while true; do
		did_work=false
		run_benchmarks
		schedule_javascript_main_job bun
		schedule_javascript_main_job node
		run_queued_jobs

		if ! $daemon_mode; then
			break
		fi
		if ! $did_work; then
			log "Caught up; sleeping ${POLL_INTERVAL}s"
			sleep "$POLL_INTERVAL"
		fi
	done
}

main "$@"
