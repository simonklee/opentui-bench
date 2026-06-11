#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
readonly REPO_DIR
readonly CONFIG_DIR="$HOME/.config/opentui-bench"
readonly SYSTEMD_DIR="$HOME/.config/systemd/user"
readonly LIBEXEC_DIR="$HOME/.local/libexec/opentui-bench"
readonly ENV_FILE="$CONFIG_DIR/worker.env"

mkdir -p "$CONFIG_DIR" "$SYSTEMD_DIR" "$LIBEXEC_DIR"
chmod 700 "$CONFIG_DIR"

if [[ ! -f "$ENV_FILE" ]]; then
	printf '%s\n' "Missing $ENV_FILE" >&2
	printf '%s\n' 'Create it with API_KEY=<key> before installing the service.' >&2
	exit 1
fi
chmod 600 "$ENV_FILE"

install -m 644 "$REPO_DIR/deploy/systemd/opentui-bench-worker.service" \
	"$SYSTEMD_DIR/opentui-bench-worker.service"
install -m 755 "$REPO_DIR/scripts/run-benchmarks.sh" \
	"$LIBEXEC_DIR/run-benchmarks.sh"
sudo loginctl enable-linger "$USER"
systemctl --user daemon-reload
systemctl --user enable --now opentui-bench-worker.service
systemctl --user --no-pager --full status opentui-bench-worker.service
