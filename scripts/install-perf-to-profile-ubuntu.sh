#!/usr/bin/env bash
set -euo pipefail

if command -v perf_to_profile >/dev/null 2>&1; then
	echo "perf_to_profile already installed at $(command -v perf_to_profile)"
	exit 0
fi

sudo apt-get update
sudo apt-get install -y g++ git libelf-dev libcap-dev curl

if ! command -v bazel >/dev/null 2>&1; then
	if ! command -v bazelisk >/dev/null 2>&1; then
		arch=$(uname -m)
		case "$arch" in
		x86_64) bazelisk_arch="amd64" ;;
		aarch64 | arm64) bazelisk_arch="arm64" ;;
		*)
			echo "Unsupported architecture: $arch" >&2
			exit 1
			;;
		esac

		bazelisk_version="v1.20.0"
		bazelisk_url="https://github.com/bazelbuild/bazelisk/releases/download/${bazelisk_version}/bazelisk-linux-${bazelisk_arch}"
		sudo curl -fsSL -o /usr/local/bin/bazelisk "$bazelisk_url"
		sudo chmod 0755 /usr/local/bin/bazelisk
	fi

	sudo ln -sf "$(command -v bazelisk)" /usr/local/bin/bazel
fi

workdir=$(mktemp -d)
cleanup() {
	rm -rf "$workdir"
}
trap cleanup EXIT

git clone https://github.com/google/perf_data_converter.git "$workdir/perf_data_converter"
cd "$workdir/perf_data_converter"

bazel build src:perf_to_profile
sudo install -m 0755 bazel-bin/src/perf_to_profile /usr/local/bin/perf_to_profile

perf_to_profile --help >/dev/null 2>&1

echo "perf_to_profile installed to /usr/local/bin/perf_to_profile"
