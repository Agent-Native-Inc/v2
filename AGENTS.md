# Working on v2

- Build the diff page before any Go command: `(cd web && npm ci && npm run build)`. The binary embeds `cmd/v2/dist`, so `go build`, `go vet` and the tests fail without it.
- Run `scripts/check.sh` before you commit. It runs every linter and formatter that CI runs.
- Tests are end to end only. Each one in `e2e/` drives the built `v2` binary in a temp folder. Don't add unit tests.
- Run `go test ./...` after any change. `scripts/coverage.sh` lists the code no test runs; new code should not show up there. A change to what v2 writes also needs a check that a release binary still reads it, and that it reads what a release binary wrote.
- `cmd/v2/agents_section.md` is what `v2 init` puts in a user's project. It isn't for working on this repo.
- Write the reason behind a design choice in `docs/decisions.md`.
