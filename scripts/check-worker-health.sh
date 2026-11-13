#!/usr/bin/env bash
#
# check-worker-health.sh checks that the benchmark worker is running and
# keeping up with opentui main. It prints one line per check and exits 1 if
# any check fails.
#
# Usage: check-worker-health.sh [ssh-host]

set -o errexit
set -o nounset
set -o pipefail

: "${API_URL:=https://opentui-bench.fly.dev}"
: "${OPENTUI_URL:=https://github.com/anomalyco/opentui.git}"
# A blobless clone of opentui, used only to measure how far the worker lags.
: "${OPENTUI_CLONE:=${XDG_CACHE_HOME:-$HOME/.cache}/opentui-bench/opentui}"
: "${MAX_LAG_HOURS:=6}"
: "${MAX_DISK_PERCENT:=85}"
# A failed queued job also exits the worker, so allow a few exits per day.
# A crash loop exits about 1,400 times per day.
: "${MAX_EXITS_PER_DAY:=5}"
: "${MAX_FAILED_JOBS_PER_DAY:=3}"

if (($# > 1)); then
	printf 'Usage: %s [ssh-host]\n' "${0##*/}" >&2
	exit 2
fi
readonly opentui_repo=$OPENTUI_CLONE
readonly ssh_host=${1:-hetzner-opentui}
failures=0

pass() { printf 'ok    %s\n' "$*"; }
fail() {
	printf 'FAIL  %s\n' "$*"
	failures=$((failures + 1))
}

check_lag() {
	local recorded pending count oldest age_hours
	if [[ ! -d "$opentui_repo" ]]; then
		git clone --quiet --filter=blob:none --no-checkout "$OPENTUI_URL" "$opentui_repo"
	fi
	git -C "$opentui_repo" fetch --quiet origin main
	recorded=$(curl --fail --silent --show-error --max-time 30 \
		"$API_URL/api/latest-commit?branch=main" | jq -er '.commit_hash_full')
	if ! git -C "$opentui_repo" merge-base --is-ancestor "$recorded" origin/main 2>/dev/null; then
		fail "latest recorded commit ${recorded:0:9} is not on origin/main"
		return
	fi
	count=$(git -C "$opentui_repo" rev-list --count "$recorded..origin/main")
	if ((count == 0)); then
		pass "caught up with main at ${recorded:0:9}"
		return
	fi
	pending=$(git -C "$opentui_repo" log --reverse --format='%ct %h' "$recorded..origin/main")
	oldest=${pending%%$'\n'*}
	age_hours=$((($(date +%s) - ${oldest%% *}) / 3600))
	if ((age_hours > MAX_LAG_HOURS)); then
		fail "$count unrecorded main commits; oldest ${oldest#* } is ${age_hours}h old (max ${MAX_LAG_HOURS}h)"
	else
		pass "$count unrecorded main commits; oldest is ${age_hours}h old"
	fi
}

check_host() {
	local report active exits disk
	if ! report=$(ssh -T -o BatchMode=yes -o ConnectTimeout=30 "$ssh_host" bash -s <<'EOF'
set -o errexit -o nounset -o pipefail
unit=opentui-bench-worker.service
printf 'active=%s\n' "$(systemctl --user is-active "$unit" || true)"
printf 'exits=%s\n' "$(journalctl --user -u "$unit" --since '24 hours ago' --no-pager -o cat |
	grep -c 'Main process exited' || true)"
printf 'disk=%s\n' "$(df --output=pcent / | tail -n 1 | tr -dc 0-9)"
EOF
	); then
		fail "cannot reach $ssh_host over ssh"
		return
	fi
	active=$(sed -n 's/^active=//p' <<<"$report")
	exits=$(sed -n 's/^exits=//p' <<<"$report")
	disk=$(sed -n 's/^disk=//p' <<<"$report")

	if [[ "$active" == active ]]; then
		pass "worker service is active"
	else
		fail "worker service is ${active:-unknown}"
	fi
	if ((exits > MAX_EXITS_PER_DAY)); then
		fail "worker exited $exits times in 24h (max $MAX_EXITS_PER_DAY)"
	else
		pass "worker exited $exits times in 24h"
	fi
	if ((disk > MAX_DISK_PERCENT)); then
		fail "disk is ${disk}% full (max ${MAX_DISK_PERCENT}%)"
	else
		pass "disk is ${disk}% full"
	fi
}

check_jobs() {
	local since failed count
	since=$(date -u -d '24 hours ago' +%Y-%m-%dT%H:%M:%SZ)
	failed=$(curl --fail --silent --show-error --max-time 30 "$API_URL/api/jobs?limit=200" |
		jq -r --arg since "$since" '.[]
			| select(.status == "failed" and (.completed_at // .created_at) >= $since)
			| "      #\(.id) \(.benchmark_kind)/\(.js_runtime // "") \(.commit_hash[0:9]): \(.error // "" | split("\n")[0])"')
	count=$(grep -c . <<<"$failed" || true)
	if ((count > MAX_FAILED_JOBS_PER_DAY)); then
		fail "$count jobs failed in 24h (max $MAX_FAILED_JOBS_PER_DAY)"
	else
		pass "$count jobs failed in 24h"
	fi
	if ((count > 0)); then
		printf '%s\n' "$failed"
	fi
}

check_lag
check_host
check_jobs

if ((failures > 0)); then
	printf '\n%d checks failed\n' "$failures"
	exit 1
fi
