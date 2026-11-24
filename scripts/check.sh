#!/bin/sh
# Every check CI runs except the tests: scripts/check.sh from the repo root.
# Needs go, node, golangci-lint, govulncheck, shellcheck, shfmt, actionlint, zizmor, typos and gitleaks.
set -eu

[ -f cmd/v2/dist/index.html ] || (cd web && npm ci && npm run build)

echo "== go"
go mod tidy -diff
GOOS=linux golangci-lint run ./...
GOOS=darwin golangci-lint run ./...
golangci-lint fmt --diff ./...
govulncheck ./...

echo "== web"
(cd web && npm run typecheck && npm run lint && npm run fmt:check)

echo "== scripts and workflows"
shellcheck scripts/*.sh
shfmt -i 2 -d scripts/*.sh
actionlint
zizmor --min-severity low .github

echo "== text"
typos
gitleaks dir . --no-banner --redact

echo "all checks passed"
