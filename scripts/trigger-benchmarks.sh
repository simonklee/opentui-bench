#!/usr/bin/env bash
# Fire-and-forget benchmark trigger
# Launches run-benchmarks.sh in background and exits immediately
# Used by GitHub Actions to avoid waiting for long-running benchmark jobs

set -euo pipefail

# Ensure PATH includes required binaries (SSH sessions don't load profiles)
export PATH="$HOME/.bun/bin:$HOME/.cargo/bin:$HOME/anyzig:$HOME/.fly/bin:/usr/local/go/bin:$PATH"

readonly SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
readonly RUN_SCRIPT="$SCRIPT_DIR/run-benchmarks.sh"
readonly LOG_FILE="$HOME/benchmark.log"
readonly PID_FILE="$HOME/benchmark.pid"

log() {
	echo "[$(date -Iseconds)] $*" | tee -a "$LOG_FILE"
}

if [[ -f "$PID_FILE" ]]; then
	pid=$(cat "$PID_FILE")
	if kill -0 "$pid" 2>/dev/null; then
		log "Benchmarks already running (pid $pid), skipping"
		exit 0
	fi
	rm -f "$PID_FILE"
fi

# Update the repo to get latest scripts (safe since no benchmark is running)
log "Updating opentui-bench repo..."
cd "$SCRIPT_DIR/.."

# Save hash of current script before updating
old_hash=$(md5sum "$0" 2>/dev/null | cut -d' ' -f1 || echo "")

if git fetch origin && git reset --hard origin/main; then
	# Check if this script changed and re-exec if so
	new_hash=$(md5sum "$0" 2>/dev/null | cut -d' ' -f1 || echo "")
	if [[ -n "$old_hash" && "$old_hash" != "$new_hash" ]]; then
		log "Script updated, re-executing..."
		exec "$0" "$@"
	fi
else
	log "Warning: git update failed, continuing with existing scripts"
fi

log "Triggering benchmark run in background"

# Launch in background, detached from session
# Explicitly pass FLY_API_TOKEN and PATH to ensure they survive detachment
FLY_API_TOKEN="${FLY_API_TOKEN:-}" PATH="$PATH" nohup "$RUN_SCRIPT" >>"$LOG_FILE" 2>&1 &
echo $! >"$PID_FILE"

log "Started benchmark process (pid $!)"
