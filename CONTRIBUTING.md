# Contributing

Thanks for helping. Open an issue before a large change, so we can agree on the approach first.

## Build

```sh
(cd web && npm ci && npm run build)   # the v2 diff page; rebuild after any change in web/
go build -o v2 ./cmd/v2
```

For the page, run `v2 diff --port 7777 --no-open` in any project, then `cd web && npm run dev`. Vite proxies to it.

## Check

```sh
scripts/check.sh
```

It runs every lint, format, vulnerability, spelling and secret check that CI runs. The script lists the tools it needs. CI blocks a merge on any of them.

Rules are strict on purpose. If a rule is wrong for one line, disable it on that line with a reason. If it's wrong everywhere, say so in your pull request.

## Test

```sh
go test ./...
```

Tests are end to end. They build the real `v2` binary and drive it in temporary projects (`e2e/`). There are no unit tests, so add an e2e test for any change in behavior. Tests need git and ffmpeg. Some Linux disk tests run only as root.

## Pull requests

- One change per pull request, with a test that fails without it.
- Say what was wrong, why the change is right and how you checked it.
- `docs/decisions.md` records design decisions. Add an entry when you make one.
