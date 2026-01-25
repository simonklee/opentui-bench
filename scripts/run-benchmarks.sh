#!/usr/bin/env bash
# shellcheck disable=SC2034
#
# run-benchmarks.sh - Orchestrate benchmark runs for opentui
#
# Usage: run-benchmarks.sh [options]
#
# This script manages the benchmarking process:
# 1. Sets up repositories (opentui-bench, opentui)
# 2. Syncs the benchmark database from Fly.io
# 3. Identifies commits to benchmark
# 4. Runs benchmarks using 'bench record'
# 5. Uploads results back to Fly.io
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
readonly DB_FILE="$BENCH_REPO/public-opentui.db"
readonly LOG_FILE="$HOME/benchmark.log"
readonly FLY_APP="opentui-bench"

# Export PATH to include necessary binaries
export PATH="$HOME/.cargo/bin:$HOME/anyzig:$HOME/.fly/bin:/usr/local/go/bin:$PATH"

# Global variables
USE_COLOR=true
verbose=false
dry_run=false
cron_mode=false
COMMAND=""

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
	local msg="[$(date -Iseconds)] $*"
	echo -e "$msg" | tee -a "$LOG_FILE"
}

# err writes to stderr and append to log file
err() {
	local msg="[$(date -Iseconds)] ERROR: $*"
	echo -e "${RED}$msg${NC}" >&2
	echo "$msg" >>"$LOG_FILE"
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
fi

SCRIPT_LOCK=""
lock_acquire() {
	local lock_dir="/tmp/${SCRIPT_NAME}.${UID}.lock"
	if mkdir "$lock_dir" 2>/dev/null; then
		SCRIPT_LOCK="$lock_dir"
		cleanup_register "lock_release"
		info "Acquired script lock: $lock_dir"
	else
		# Check if the process holding the lock is still alive
		# This is a basic stale lock check, could be more robust
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

	# Install flyctl if missing
	if ! command -v flyctl &>/dev/null; then
		log "Installing flyctl..."
		curl -L https://fly.io/install.sh | sh
		export FLYCTL_INSTALL="$HOME/.fly"
		export PATH="$FLYCTL_INSTALL/bin:$PATH"
	fi
	return 0
}

fly_machine_running() {
	local app="$1"
	# Check if any machine is started or running using jq
	flyctl machine list --app "$app" --json 2>/dev/null |
		jq -e 'any(.[]; (.state // "") | ascii_downcase | . == "started" or . == "running")' >/dev/null
}

fly_machine_first_id() {
	local app="$1"
	# Get the first machine ID found
	flyctl machine list --app "$app" --json 2>/dev/null |
		jq -r 'map(select(.id)) | .[0].id // empty'
}

ensure_fly_app_running() {
	local app="$FLY_APP"
	local max_attempts=30
	local attempt=0

	if fly_machine_running "$app"; then
		return 0
	fi

	log "Fly app is not running, starting it..."
	local machine_id
	machine_id="$(fly_machine_first_id "$app")"

	if [[ -n "$machine_id" ]]; then
		log "Starting machine $machine_id..."
		flyctl machine start "$machine_id" --app "$app" 2>&1 || true
	else
		log "No machine found, scaling up..."
		flyctl scale count 1 --app "$app" --yes 2>&1 || true
	fi

	while ((attempt < max_attempts)); do
		if fly_machine_running "$app"; then
			log "Fly app is now running"
			return 0
		fi
		((attempt++))
		sleep 2
	done

	err "Fly app failed to start after ${max_attempts} attempts"
	return 1
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
	make build

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

sync_db_down() {
	log "Downloading DB from Fly.io..."
	cd "$BENCH_REPO"
	ensure_fly_app_running || true

	if [[ -f "$DB_FILE" ]]; then
		chmod u+w "$DB_FILE" || true
	fi

	local tmp_db
	tmp_db="$(mktemp "${DB_FILE}.XXXXXX")"
	rm -f "$tmp_db" # flyctl sftp refuses to overwrite existing files

	if flyctl ssh sftp get /data/bench.db "$tmp_db" --app "$FLY_APP"; then
		log "Downloaded DB from Fly"
		mv -f "$tmp_db" "$DB_FILE"
		chmod u+w "$DB_FILE" || true
	else
		rm -f "$tmp_db"
		err "Failed to download DB from Fly"
		return 1
	fi
}

sync_db_up() {
	log "Uploading updated DB to Fly.io..."
	cd "$BENCH_REPO"
	ensure_fly_app_running

	local machine_id
	machine_id="$(fly_machine_first_id "$FLY_APP")"

	if [[ -z "$machine_id" ]]; then
		err "No running machine found to upload DB"
		return 1
	fi

	# Remove remote DB first as sftp put doesn't overwrite
	log "Removing existing remote DB on machine $machine_id..."
	flyctl ssh console --machine "$machine_id" --app "$FLY_APP" -C "rm -f /data/bench.db" || true

	if flyctl ssh sftp put "$DB_FILE" /data/bench.db --machine "$machine_id" --app "$FLY_APP"; then
		log "Uploaded DB to Fly"
	else
		err "Failed to upload DB to Fly!"
		return 1
	fi

	log "Restarting Fly app..."
	flyctl apps restart "$FLY_APP"
}

reset_opentui() {
	if [[ -d "$OPENTUI_REPO" ]]; then
		cd "$OPENTUI_REPO"
		git reset --hard HEAD 2>/dev/null || true
	fi
}

run_benchmarks() {
	cd "$OPENTUI_REPO"

	# Find latest recorded commit
	local latest_recorded
	latest_recorded=$("$BENCH_REPO/bench" latest-commit --db "$DB_FILE" 2>/dev/null || echo "")

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

	log "Processing commit: ${next_commit:0:7}"

	# Register cleanup to reset repo if we fail during processing
	cleanup_register "reset_opentui"

	# Checkout the commit
	git checkout "$next_commit"

	# Run benchmarks
	cd "$BENCH_REPO"
	# Samples set to 3 for dedicated hardware (adjust as needed)
	if $dry_run; then
		log "Dry run: would exec ./bench record ..."
	else
		./bench record --repo "$OPENTUI_REPO" --db "$DB_FILE" --samples 3 --notes "Hetzner CCX13" --profile cpu
	fi

	# Reset opentui repo
	reset_opentui

	# Upload results
	if ! $dry_run; then
		sync_db_up
	fi

	log "Benchmark run complete for ${next_commit:0:7}"
}

# --- Main ---

show_usage() {
	cat <<EOF
Usage: ${SCRIPT_NAME} [options]

Options:
    -v, --verbose    Enable verbose output
    -n, --dry-run    Show what would be done without doing it
    -h, --help       Show this help message
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

	setup_repos

	if $dry_run; then
		log "Dry run: Skipping DB download"
	else
		sync_db_down
	fi

	run_benchmarks
}

main "$@"
