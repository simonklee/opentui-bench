#!/bin/sh
# Queue the daily benchmark worker health check in an OpenCode session. Run by
# opentui-bench-health.timer. Without a running OpenCode service it skips
# quietly; the next day tries again.
set -eu
session=${OPENTUI_BENCH_SESSION:?set OPENTUI_BENCH_SESSION to the session ID}
oc=${OPENCODE_BIN:-$HOME/.local/bin/opencode}
url=$("$oc" service status 2>/dev/null | head -n 1 || true)
case "$url" in
http*) ;;
*)
	echo "opentui-bench-health: no running OpenCode service; skipped" >&2
	exit 0
	;;
esac
stamp=$(date -u +%Y-%m-%dT%H:%MZ)
text="Scheduled worker health check $stamp (opentui-bench-health.timer): in /home/simon/src/opentui-bench run scripts/check-worker-health.sh. If every check passes, reply with the output in one line. If any check fails, investigate on hetzner-opentui (worker journal, ~/benchmark.log, disk), fix what is safe to fix, and report the cause and what you changed. Ask before pushing, deleting data, or anything else hard to undo."
"$oc" api post "/api/session/$session/prompt" --data "$(jq -cn --arg text "$text" '{text: $text, delivery: "queue"}')" >/dev/null
echo "opentui-bench-health: queued check $stamp in $session via $url"
