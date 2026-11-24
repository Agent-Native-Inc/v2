#!/usr/bin/env bash
# Lists every function the e2e tests never run, from a binary built with coverage.
set -euo pipefail
cd "$(dirname "$0")/.."
dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT
GOCOVERDIR="$dir" go test ./e2e/ -count=1
go tool covdata func -i="$dir" | sort -k3 -n
go tool covdata percent -i="$dir"
