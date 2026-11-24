# v2

v2 saves every version of a video project. Code and text go through git. Footage, music and renders go into v2's own store. Any version comes back exactly.

It's built for making videos with a coding agent such as Claude Code or Codex. `v2 init` teaches the agent to save a version after each edit, with the prompt that asked for it. `v2 diff` opens every version in the browser, so you can watch one, compare two and leave timed comments for the agent to fix. `v2 push` shares them on [v2.video](https://v2.video).

## Install

macOS and Linux:

```sh
curl -fsSL https://v2.video/install.sh | sh
```

v2 needs git and ffmpeg. Releases are signed, and `v2 update` checks the signature before it replaces itself.

## Use

```sh
cd your-video-project   # or a new empty folder
v2 init                 # set up the project and teach your agent to use v2
claude                  # ask for edits as usual; the agent saves a version after each one
v2 log                  # list versions, newest first
v2 diff                 # watch, compare and comment in the browser
v2 restore 3            # bring back version 3 exactly, saved as a new version
v2 push                 # upload to v2.video and print a private link
```

Run `v2 --help` for every command.

## Build from source

Go (see `go.mod`) and Node 24:

```sh
(cd web && npm ci && npm run build)   # the v2 diff page, embedded in the binary
go build -o v2 ./cmd/v2
```

`go install` doesn't work: the page has to be built first, and Go would name the binary `cmd`.

See [CONTRIBUTING.md](CONTRIBUTING.md) to run the checks and tests, and [docs/decisions.md](docs/decisions.md) for why v2 works the way it does.

## License

[MIT](LICENSE)
