# v2: design decisions

This file records v2's design decisions and the reasons for them, newest at the bottom.

## Scope (2026-10-22)
- v2 started as a local version-control CLI for one user. Cloud, forking, sharing and a desktop app were out of scope.
- **Why:** the local tool is the part a user touches every day.

## Language and platform (2026-10-22)
- The CLI is called `v2` and written in Go. It shells out to the installed `git` and prints a clear error if git is missing.
- It runs on macOS and Linux (Linux added 2026-11-12). On the Mac, files are materialized with APFS clones (`clonefile`), so copies are instant and take no extra disk. Linux uses `FICLONE` (btrfs, XFS). ext4 has no reflink, so saves and imports there copy, like exFAT on the Mac. Restore and clone tell the hash cache what they placed, so the next save doesn't copy it again.
- **Why:** a single static binary, git does the hard text diffing, and clones keep huge video files from doubling disk use.

## Two trackers (2026-10-22)
- **git** tracks the "recipe". That is every text file (code, cut lists, transcripts, prompts, lockfiles).
- **The content store** (`.v2/objects/ab/cd…`) holds every binary exactly once, named by its sha256. git never sees these files.
- **`assets.json` is git-tracked.** It maps each binary's path to `{sha256, size, source}`, so git history knows which exact bytes belong to each version without storing them.
- `source` is `imported` (came in through `v2 import`) or `derived` (anything the agent made).
- **Why:** git chokes on GB-sized binaries. Hashing gives dedup for free and lets restore verify it got the exact bytes back.

## On-disk layout (2026-10-22)
```
project/
├── <agent's files, any layout>
├── .git/
└── .v2/
    ├── objects/      binaries by sha256          (git-excluded)
    ├── assets.json   path → {sha256,size,source} (git-tracked)
    ├── commit.json   per-save metadata           (git-tracked)
    ├── hashcache     path+size+mtime → sha256    (not tracked)
    └── ignore        node_modules, caches, .DS_Store
```

## Commits (2026-10-22)
- A save is called a "commit", not a "checkpoint". `commit.json` stores the prompt, a plain-language `--changes` summary with timestamps, the assembly command, the ffmpeg version and the preview path.
- Each save gets a git tag `vN`.
- Restore never rewrites history. It brings back exact bytes, then auto-saves as a new version.
- **Why:** history only moves forward, so every version stays restorable.

## Generated assets and previews (2026-10-22)
- Generated assets (Blender, ElevenLabs, FLUX, Suno) are never re-rendered. They're stored as bytes.
- Only the final assembly can be rebuilt from the recipe. Its output (`final.mp4`) is kept as a deletable preview cache.
- **Why:** generative tools aren't deterministic, and re-running them costs money.

## No operation schema (2026-10-22)
- Agents read raw diffs of ffmpeg and Remotion code, so there's no shared timeline format. Human-readable changes come from `--changes`. A framework layer is parked.
- **Why:** `v2` must work with whatever stack the agent uses.

## Agents learn the tool from a skill (2026-10-22)
- A skill file teaches agents to use `v2`, and `v2 init` also writes `AGENTS.md` as the fallback for Codex.

## CLI framework is cobra (2026-10-23)
- Use `cobra` (which brings `pflag`), not the stdlib `flag` package.
- **Why:** stdlib `flag` stops at the first positional argument, so `v2 import a.mp4 --to x` silently misreads `--to`. Cobra finds flags anywhere and leaves room to grow.

## Module path is `github.com/Agent-Native-Inc/v2-cli` (2026-10-23)
- The repo is `v2-cli`. The binary and the local folder are still `v2`.
- **Why:** Go reads a module path ending in `/v2` as "major version 2", which breaks `go install`.

## CLI behaviour (2026-10-23)
- Bare `v2` prints help and exits 0.
- Unknown commands and errors print to stderr and exit 1.
- **Why:** agents decide success from the exit code.

## Importing across disks copies, and says so (2026-10-23)
- `clonefile` only works within one APFS volume. When importing from an SD card or external drive, v2 falls back to a real byte copy, prints `copying N GB from another disk…` and shows progress. On Linux it prints `copying N GB…`, because most Linux disks can't clone at all.
- **Why:** a silent fallback would make a 50 GB import look frozen for minutes.

## Put clones first, hashes the clone, always replaces (2026-10-23)
- `Put` clones the file into a temp name, hashes the clone, then atomically renames it to `objects/<sha>`, replacing any existing object.
- **Why:** hashing the original would race with an agent still writing it. You can't check whether an object exists before hashing, because the hash is its name. Replacing costs nothing extra and heals a corrupted object.
- `Get` re-hashes before placing the file, so corrupted bytes are never handed back.

## git errors show git's own message (2026-10-23)
- A failed git command returns `git <subcommand>: <git's message>`. It uses stderr, or stdout when stderr is empty (git prints "nothing to commit" to stdout).
- Missing git or ffmpeg says how to install it. On the Mac that is `xcode-select --install` and `brew install ffmpeg`. On Linux it is the command for the package manager on PATH (apt, dnf, pacman, apk, zypper), without `sudo` for root, else "with your package manager". The Mac message also covers the `/usr/bin/git` stub that exists without Command Line Tools.
- **Why:** agents act on git's exact wording, and the prefix tells them which step failed.

## init leaves an existing repo alone (2026-10-23)
- If `.git` exists, init keeps its history and never edits `.gitignore`. v2's exclusions go in `.git/info/exclude`, which is local and never committed.
- Init only creates files that are missing, so running it twice changes nothing.
- **Why:** a project may already be a git repo, and v2 must not rewrite it.

## assets.json paths are relative, fingerprints come from bytes only (2026-10-23)
- Keys in `assets.json` are paths relative to the project root, with forward slashes (`assets/intro.mp4`).
- The sha comes only from a file's bytes, never from its path or the machine it's on.
- **Why:** moving the project, cloning it to another machine or renaming a file never changes a fingerprint. Only the map key changes.

## Skip list is separate from .gitignore (2026-10-23)
- Media projects often gitignore `*.mp4` and footage folders because git can't handle them. v2 must still store those files, so it can't treat `.gitignore` as "don't save".
- v2's skip list (`node_modules/`, `.DS_Store`, caches) means "don't save anywhere".

## Imports land in assets/<name>, collisions refuse (2026-10-23)
- `v2 import <files>` puts each file at `assets/<basename>`.
- If `assets/<name>` already holds different bytes, import refuses and points to `--as <name>`. If the bytes are identical, it's a no-op that still makes sure the file is stored and recorded. `--as` takes one file and a plain name (no folders).
- **Why (collision default):** suffixing (`C0001-2.MP4`) would leave the agent's code pointing at the old clip, and overwriting would silently swap footage under the recipe. Refusing never does either, and `--as` unblocks the agent in one step.

## Copy fallback everywhere, not just import (2026-10-23)
- Tested on real disk images, `clonefile` returns `EXDEV` across volumes and `ENOTSUP` on exFAT even within one volume. It never copies by itself.
- `store.Clone` clones and falls back to a byte copy on exactly those two errors. Any other error stops. Import, `Put` and `Get` all use it, so a project on an exFAT drive works too. Files of 100 MB or more print `copying N from another disk…` (Linux prints `copying N…`) and a progress line every 10%.
- **Why:** working on any drive matters more than speed. The cost is that on exFAT every save copies bytes into `.v2/objects`, which takes extra disk and time.

## Binary means extension list, then NUL sniff (2026-10-23)
- Known media and asset extensions (`.mp4 .mov .wav .png .exr .blend …`, in `internal/walk`) are always binary. Anything else is binary if its first 8 KB contain a zero byte, which is git's rule.
- **Why:** the list catches media that happens to have no NUL bytes up front, and the sniff catches formats nobody listed (`.aep`, files with no extension). The speed difference doesn't matter. Hashing dominates.

## Hash cache key is size + mtime + ctime + inode (2026-10-23)
- A cached hash is trusted only if all four match. Otherwise the file is re-classified and re-hashed.
- **Why:** size + mtime misses a file replaced with the same size and a copied-back mtime (`cp -p`, `rsync -t`, unzip). That would save the wrong hash and make restore return the wrong bytes. The cost is that copying the project folder re-hashes everything once, which is slow anyway.
- Measured on a Mac, hashing 3 GB took 1.15 s (about 4 s per 10 GB, not the 10 to 30 s estimated earlier). A second scan takes 0.00 s. Touching one file re-hashes only that file.

## Hash cache: a file as new as the disk's clock is read again (2026-11-13)
- Each scan first asks the project's disk for its time. That is the mtime of `.v2/clock`, which it makes and deletes. It stays in `.v2/`, not `.v2/objects/`, because `.v2/objects` can be a link to another disk (a big store moved off a full disk), and that disk's clock is the wrong one. v2's `.gitignore` lists `.v2/clock` and `.v2/hashcache.tmp`, and the next scan reuses both names, so a file left by a killed v2 goes away.
- A file whose mtime is not older than the disk's time gets `Racy` in its cache entry. The next scan reads it and compares the hash instead of trusting the key, unless the file's mtime is still after the disk's time. No write made before then can give that mtime unless a tool copies the file's date (`cp -p`, `rsync -t`, unzip), so the key still holds. On FAT, the earlier code missed such a copy at any date, so this is no worse. When the hash matches and the mtime is now older than the disk's time, `Racy` goes away. This is git's "racily clean" rule, with the time taken before the files are read instead of after.
- **Why:** FAT steps mtime by 2 seconds and keeps the inode. So a render redone to the same size right after a save or a restore had the same key, and save said `nothing changed`. The disk's own time has the disk's precision and, on a network disk, the server's clock, so no number is assumed.
- The cost is that a file saved in the same step as its last write is read once more on a later save (150 MB took 0.07 s on a Linux FAT disk image and 0.06 s on a Mac). A file dated in the future (a camera with its clock ahead) is read once, on the first save after that date passes. The read happens in place, so a disk without clones doesn't copy the video into the store again.

## Skip list stays in .v2/ignore (2026-10-23)
- One pattern per line. `name/` skips folders with that name anywhere, a pattern with `/` matches from the project root, and anything else matches a file or folder name anywhere. `.git` and `.v2` are always skipped. Symlinks are left to git.
- Defaults are `node_modules/`, `.DS_Store` and `*.v2tmp` (leftovers from a crashed import).
- **Why:** `init` already writes this file, and `.gitignore` can't be used (see above).

## Assembly command is a config file, a flag overrides (2026-10-23)
- `init` writes `.v2/config.json` with `"assemble": ""` (git-tracked). `save --assemble "<cmd>"` overrides it for that one save only. If neither is set, save warns but still saves.
- **Why:** the config is set once and can't be forgotten. The flag covers a one-off render without editing the file.

## What save does (2026-10-23)
- `v2 save -m "<what was asked>" --changes "<what changed, with timestamps>"`. Both are required, because `log` and the user read them.
- Save walks the project. Every binary goes into the store (hashed once, through the walk's hash callback). Binaries whose cached object is missing get stored again.
- `assets.json` is rewritten to list exactly today's binaries. `source` stays `imported` only while the bytes match what was imported. Otherwise it is `derived`.
- Binary paths go into a v2-managed block in `.git/info/exclude`, rewritten every save and escaped literally (`[`, `*`, `?`, trailing spaces). **Why:** appending forever would keep a deleted binary's path hidden from git, so a text file created there later would be silently untracked.
- `commit.json` records version, message, changes, assemble, the ffmpeg on PATH (`ffmpeg version X`) and `saved_at` (UTC).
- If nothing changed, save prints `nothing changed since the last save`, exits 0 and makes no tag. Tags are `v<max+1>`.
- `--preview` was left for later, when the preview cache got its meaning.

## log shows what each version changed (2026-10-23)
- `v2 log` lists versions newest first, sorted by number (v10 after v9, not after v1). Each one shows the time, message, `--changes`, the recipe files it changed (`M src/Video.tsx`, `A …`, `D …`, leaving out `.v2/`), the binaries it added, changed or removed (`+ clip.mov`, `M …`, `- …`), and the assembly command.
- **Why:** the recipe is what ties a version together, so log puts recipe changes next to the plain-words summary. For full detail an agent can run `git diff v1 v3`.

## Prior art: git-lfs and git-annex (2026-10-23)
- Both already layer a content store on git, which is v2's storage half. They were tested side by side on a 2 GB clip (v1), replaced with 500 MB (v2), then a restore of v1.
- git-lfs and unlocked annex add about 4.3 GB (a store copy, then a full rewrite on checkout). Locked annex adds about 0 but makes files read-only symlinks, so an agent re-rendering in place gets `permission denied`. `annex.thin` adds about 0, but an in-place edit destroyed v1's stored bytes. v2 adds 0 MB with normal files, because of APFS clones.
- Neither has the "why" of a version (prompt, `--changes`, assembly command), automatic binary detection, or a `log` with recipe and asset changes.
- **Decision:** keep v2's own store.

## Restore (2026-10-23)
- `v2 restore vN` works in four steps.
  1. It refuses if anything is unsaved.
  2. It pulls every binary that differs out of the store into a temp file, verified by hash.
  3. Only then does it remove binaries vN doesn't have, run `git restore --source=vN` (no-overlay, so text files vN doesn't have are removed) and rename the binaries into place.
  4. It saves the result as a new version, "restore vN".
- **Unsaved changes are refused (2026-10-24, replacing an earlier auto-save).** Restore lists what is unsaved (`M src/Video.tsx`, `+ out/new.mp4`, `- clip.mov`, up to 10) and says to run `v2 save` first. It touches nothing and makes no version. **Why:** the user or agent has to decide consciously about unsaved work, and auto-saving hides that choice. AGENTS.md tells agents to ask the user before saving or discarding.
- **Failure modes tested.** (a) Files from later versions linger, or newer versions become unrestorable. (b) Unsaved work is overwritten (tested for edited, new and deleted text files and binaries). (c) A corrupted or missing object puts bad bytes in the folder, or leaves it half-restored. Each test was proven to fail when the matching safeguard is removed.
- Measured: restoring a 2 GB clip costs 0 MB of disk and leaves a normal, editable file.

## The preview is a stored binary plus a pointer (2026-10-23)
- `.v2/config.json` has `"preview": "out/final.mp4"`, and `save --preview` overrides it for one save. `commit.json` records the preview path only if that file exists at save time. Otherwise save warns that this version has no preview.
- The preview's bytes are stored like any other binary, so `restore vN` brings back vN's exact render. Deleting `final.mp4` to free disk is allowed. The next save simply has no preview.
- **Why:** no separate cache to keep in sync, and restore already guarantees exact bytes. Re-rendering a missing preview from the `assemble` command is left for later.

## Agents learn v2 from AGENTS.md, which CLAUDE.md imports (2026-10-23)
- `v2 init` appends a marked v2 section (`cmd/v2/agents.md`, embedded in the binary) to `AGENTS.md`, and an `@AGENTS.md` line to `CLAUDE.md`. Each is added only if missing, and the user's own content is never changed.
- **Changed from the plan:** there is no separate skill file. Claude Code always loads `CLAUDE.md` but loads a skill only when its description matches, and whether Codex reads Claude-style skills is unverified. Codex reads `AGENTS.md`. One text, two entry points.
- Checked with a fresh Claude Code agent (`claude -p`, project settings only). Asked to change a title and "bring in" a music file with no mention of v2, it ran `v2 import` and `v2 save` on its own, with a timestamped `--changes`. Not yet checked with Codex.

## End-to-end check with a real agent (2026-10-23)
- A throwaway Remotion hello-world project (already a git repo) got `v2 init`, then three agent sessions that never mention v2. (1) "use this clip as the background, render". (2) "new title, white, clip at 50%, render". (3) "I liked the very first version better, go back".
- The agent imported with `v2 import`, filled in `assemble` and `preview` in `.v2/config.json`, saved after each request with timestamped `--changes`, and used `v2 restore v1` for the third. Afterwards the render, footage and code matched v1 byte for byte, the tree was clean, and Remotion's own first commit was untouched.

## .v2/ignore is also git's ignore list (2026-10-24)
- `save` (and `init`) write the `.v2/ignore` patterns into v2's block in `.git/info/exclude`, ahead of the binary paths. The patterns already use `.gitignore` syntax, so they're copied as is.
- **Why:** found in a real project. v1 had no `.gitignore`, so `restore v1` deleted the later one, and git could see `node_modules` again. The next save would have committed about 1 GB of JavaScript into git, and the agent ran `rm -rf node_modules` to prevent it. Now skipped files stay out of git no matter which version's `.gitignore` is on disk, and nothing needs deleting.
- This was hidden on the development machine because a global gitignore already ignored `node_modules/`. The end-to-end tests now run with `GIT_CONFIG_GLOBAL=/dev/null` so they behave like a fresh machine.

## Timed changes at save, and `v2 diff` (2026-10-24)
- **Goal:** a timeline of the agent's changes, where they sit in the video, a link to view, and timestamped comments that feed the next version. Several page designs were prototyped. The chosen one puts versions first (one page, a dock of versions, latest by default), the compare menu on the side, and an accurate timeline.
- **Rejected page designs.** Round 1 had tracks (an editor's layer view, where 39 caption clips read as a technical diff), ribbon (right idea, but split by layer), filmstrip (no playback) and changelog (a list, not a timeline). Round 2 had chapters (only one version visible at a time) and stories (changes below the fold, a presentation rather than a review tool).
- **`save --change`, repeatable.** Each line is a span (`0:04-0:09 text`), a moment (`0:12 text`), a cut (`cut 0:03.4 3.4s text`) or, with no time, the whole video. Times are in the new version's own timeline. `commit.json` now stores `changes: [{from, to, text}]` and `cuts: [{at, removed, text}]`. A backwards span or a malformed cut is refused before anything is saved. **Why:** it is one shell command an agent can write without a temp file, and the shape is what the page needs to place changes and map time across cuts.
- **The old form still works.** `--changes "a; b"` (hidden) is split on `;`, and old `commit.json` files whose `changes` is a string are read the same way, so versions saved before this show up on the page. Old free text with times at the end ("subtitles (0:00-0:20)") lands as a whole-video line.
- **`v2 diff`** serves the page on `127.0.0.1` only and opens the browser. Each version's video is its stored render, streamed straight from `.v2/objects` with range requests (seeking works). Nothing is re-rendered. Durations come from `ffprobe` and dock stills from one `ffmpeg` frame per render, both cached per hash. If ffmpeg is missing, the page reads the duration from the video and shows a blank still.
- **Comments stay in the browser (localStorage), and "Copy for Claude" builds the next prompt.** Saving them into the project, so an agent can read them directly, is left for later.
- **Known limit:** comparing across a restore (e.g. v4 = "restore v1", compared with v2) lists the in-between versions' changes, even though the restore undid them, and maps time 1:1.

## "preview" is now "render" (2026-10-24)
- `.v2/config.json`, `commit.json`, the `--render` flag and `v2 log` say `render`. It's the full final video, not a low-res copy, and "preview" suggested a draft. The old name is still read everywhere (config, old `commit.json`, a hidden `--preview` flag), and new saves write only `render`.
- A real low-res preview (e.g. 720p, made at save, only for `v2 diff`) is left for when renders get long or 4K.

## ffmpeg is required (2026-10-24)
- `init`, `save` and `diff` stop with "v2 needs ffmpeg … `brew install ffmpeg`" if ffmpeg or ffprobe is missing. `save` records the ffmpeg version and `diff` uses both for durations and dock stills, so running without it gave quietly worse results.

## `v2 init` refreshes the AGENTS.md section (2026-10-24)
- Running `init` again replaces the text between `<!-- v2: start -->` and `<!-- v2: end -->` with the current instructions. The user's text around it is untouched. Before, `init` skipped AGENTS.md if the section existed, so projects made before `--change`, `v2 diff` and `render` kept telling agents to use `--changes` and `preview`. It is not done on every save, because that would slip an unrelated AGENTS.md edit into a version.

## Finding: renders rebuild byte for byte from recipe + footage (2026-10-24)
- On an APFS clone of a real project, the stored renders were moved out of `.v2/objects` (restore and `v2 diff` then reported them missing). Each version's recipe was checked out, the footage cloned in, and its recorded `assemble` command run. All four came back with the **same sha256** as the stored render. v1 and v4 (`uv run scripts/render.py`, Homebrew ffmpeg 8.0.1) took 13.5 s each. v2 and v3 (`npm ci` from the lockfile, then `npx remotion render`) took 101 s and 108 s.
- This was only tested on the machine that made them. Remotion's renderer and ffmpeg come pinned through `package-lock.json` (`@remotion/compositor-darwin-arm64` 4.0.538), so v2 and v3 have a good chance elsewhere. v1 depends on the system ffmpeg version and macOS's Arial Bold. **Unverified on another machine.**
- So renders could be treated as a cache (dropped to save space, rebuilt on demand), as long as the footage and recipe are kept. Not built.

## Finding: a second project rebuilds too, except a step nobody wrote down (2026-10-25)
- The same test ran on an APFS clone of another project (4 versions, Remotion). With the stored renders removed, all four rebuilt **byte for byte** (about 1 min each) from recipe + footage.
- It also has a second derived file, `public/src.mp4`. That is a 1080p H.264 copy of the 4K HEVC footage that the Remotion code plays. The command that made it is recorded nowhere (not in `assemble`, the code or any commit), so with it removed the render fails (404 on `public/src.mp4`). The command could be recovered only because x264 writes its settings into the file (`crf=18`). `ffmpeg -i assets/IMG_2889.mov -vf scale=1920:1080 -c:v libx264 -crf 18 -c:a aac public/src.mp4` reproduces it byte for byte.
- Lesson: a version is rebuildable only if every step that makes a derived file is in the recipe. Options include an `agents.md` rule ("put every derived-file step in the assemble command or a build script") or keeping derived intermediates stored, as today.

## Renders stay as they are (2026-10-26)
- Keep storing every render and derived intermediate, as today. No `agents.md` rebuild rule, no renders-as-cache. Revisit later.

## End-to-end tests only, no unit tests (2026-10-26)
- The 4 unit test files (`cmd/v2/changes_test.go`, `internal/{gitx,store,walk}`) were removed. The end-to-end files (in `e2e/`) run the built binary. Never add unit tests. New behaviour gets an end-to-end test.
- Lost with the unit tests were `TestProbeShape` (rotated phone video shape) and the old-`commit.json` reading checks. Re-cover them end to end if those paths change.

## Framework layout for new projects (2026-10-27)
- `v2 init` in an empty folder makes `assets/imported/`, `assets/generated/`, `src/`, `scripts/`, `out/` and `README.md`, and sets `"layout": 1` in `.v2/config.json`. Existing folders are left as they are (like create-next-app). A test on 28 files showed footage and generated media can't be told apart reliably (10 of 28 placed with confidence, 1 wrong).
- `v2 save` refuses misplaced files and says where each belongs. The agent moves them and fixes references. There is no auto-move, because it could break paths. The top level holds config files only. Dot-folders (`.claude/`) are allowed. `public/` gets a hint to point Remotion or Vite at `assets/`.
- Files in `assets/` never change once saved, and a new version gets a new name. A restore is exempt, since it brings back older bytes on purpose.
- Source comes from the folder. `assets/imported/` is imported, and everything else is derived.
- `.gitignore` (committed) replaces `.git/info/exclude` in new projects, so the rules travel with the project.
- No tool lock. Projects pin packages with `package-lock.json` or `uv.lock`, and v2 stores every render and generated file, so old versions never need re-rendering. A refusing `v2.lock` and a `v2 render` were built, tested and removed as over-engineering. Tool-pinning options looked at were mise, Nix, Hermit, DotSlash and Pixi. Font determinism is out of scope.
- `commit.json` no longer records the ffmpeg version. Old files still read.

## Restore and ignored files; .v2ignore (2026-10-27)
- Bug: `v2 restore v1` brought back v1's `.gitignore`, so the restore's save committed a `.env` that was gitignored later (API keys into history), and restoring forward then deleted it from disk. Fix: restore records what git ignores before going back and keeps ignoring it in `.git/info/exclude` (local, never committed).
- `.v2ignore` at the project root lists what v2 never saves, in the same format as `.gitignore`. It is committed with the project, and agents may add tool caches to it. Older projects keep `.v2/ignore`, and both are read. A restore keeps today's `.v2ignore`. Defaults are `node_modules/ __pycache__/ .venv/ venv/ .env .env.* dist/ build/ .next/ .cache/ media/ .hyperframes/artifacts/` and temp files. `output/` and `renders/` are deliberately not ignored, so save points renders to `out/` instead of losing them.
- Why not just follow `.gitignore`? In new projects `.gitignore` lists every stored binary, so v2 would skip its own media.
- `.v2/objects/` and `.v2/hashcache` are now inside v2's block of `.gitignore`, so replacing `.gitignore` can't put the store into git.

## assets/ is media only; git skips assets/ and out/ whole (2026-10-27)
- Found in a fresh-agent test of a launch video. `agents.md` said scripts write to `assets/generated/`, so a Python script's `timeline.json` went there next to its wav, and the code imported it from `assets/`. Media and code are meant to stay apart.
- Now `assets/` holds media only and `out/` the render only. `v2 save` refuses text in either ("text the code reads goes in src/"), and `v2 import` refuses text files. `agents.md` says scripts write media to `assets/generated/` and text to `src/`.
- So the `.gitignore` block lists `/assets/` and `/out/` instead of one line per saved file. Per-file lines remain only for media elsewhere, e.g. a dot-folder.
- The default `.v2ignore` line `.hyperframes/artifacts/` only matched at the project root (the gitignore rule for a middle slash). Hyperframes kept its project in `src/hyperframes/`, so its check reports were saved. Now the line is `.hyperframes/`, which matches at any depth in both v2 and git.

## Stress test fixes (2026-10-27)
- A fresh agent used 12 tools (say, whisper, Rust, Pillow, cairosvg, Manim, MoviePy, ffmpeg, Hyperframes, Motion Canvas, node, Remotion) on one project. Nothing was lost. The friction was messages and placement.
- `.gitignore` lists only what v2 stores (`/assets/`, `/out/`, the store). The `.v2ignore` copy git needs goes to `.git/info/exclude`. One line per saved file was a mistake carried over from older projects.
- Refusals are one line per folder and reason (`music/ (312 files): …`). Media in `src/` or `scripts/` also suggests `.v2ignore` for tool scratch. `target/` is in the default `.v2ignore`.
- No big-file warning.
- `agents.md` gives each tool its own folder (`scripts/<tool>/` or `src/<tool>/`). A tool's working media (Manim's partial renders, Motion Canvas frames) goes in `assets/scratch/<tool>/`. That is inside the project, where it is easier to find than /tmp, and it is never saved, so every media file on disk lives in `assets/` or `out/`. Putting it in the tool's folder under `scripts/` was wrong, since that put media in `scripts/`. Starters' own `AGENTS.md` and `CLAUDE.md` are kept, because they help the agent use the tool.

## Independent review before v0.2.0 (2026-10-27)
An outside reviewer, given only the code, reproduced these. Each is fixed with an e2e test proven to fail without the fix.
- Default `.v2ignore` names like `media/`, `build/`, `dist/` matched at any depth, so `src/media/clips.ts` or `assets/generated/build/x.png` were silently never saved. Tool-output names are now anchored at the top (`/build/`), and v2's matcher understands a leading `/` like git.
- A starter that ran `git init` in `src/<tool>/` made git store a pointer, so its files were never versioned. Save now refuses gitlinks and says to delete that `.git/`. `agents.md` says the same.
- A symlink in `assets/` or `out/` was in neither git (ignored) nor v2 (links not followed). Save refuses it and suggests `v2 import`.
- `.svg` and `.cube` (LUTs) count as media, so they can live in `assets/`. Lottie `.json` is still treated as text. Open.
- Dotfiles (`.prettierrc.cjs`) and `bun.lockb` are allowed at the top.
- A hand-edited `.gitignore` missing v2's end marker kept losing the lines after it. Now they're kept.
- `init` says which mode it chose.
- Not fixed (pre-existing): saving in a fresh clone with no store drops assets from `assets.json` silently.

## Simplify after an outside review (2026-10-28)
Two outside reviewers, given only the code and docs, both said the core is right and the new layout layer is too heavy.
- Git matches `.v2ignore` now (`git ls-files --ignored --exclude-from`), so v2 and git always agree, and `**` and `!` work. The hand-written matcher is gone. Ignored folders come back whole (`--directory`), so `node_modules` is never listed file by file or opened.
- git 2.30 and older (Ubuntu 20.04, Debian 11) has a gap there. With `--directory` it reports nothing inside an untracked folder, and `assets/` is never tracked, so `assets/scratch/` was saved. Restore's listing of what `.gitignore` covers had the same gap. git 2.32 is fine (2.31 untested).
- So on git before 2.32 only, v2 also lists the untracked folders and asks for their ignored files one by one (`gitx.IgnoredOthers`, used by save and restore). Then it asks about each of their folders by name, which old git answers, so `remotion/node_modules/` stays one entry and not 80,000 files in restore's `.git/info/exclude` block (which made every later save 7x slower). The folder names go to git in batches, since tens of thousands of them overflow the command line, and `ls-files` has no `--pathspec-from-file`. The version check runs beside the first listing. Run before it, it cost 4.5 ms per save on macOS.
- Measured on a Remotion + Next project (80,012 files in `node_modules`), median no-change save before → after. macOS git 2.53 went 44 → 44 ms, Ubuntu 22.04 3.3 → 3.5 ms, Ubuntu 20.04 3.7 → 4.5 ms.
- Rejected: listing every ignored file one by one on every git. Save went to 423 ms on macOS and 120 ms on Ubuntu, and save failed on an ignored folder it can't read (a docker Postgres dir in `.cache/`), because v2 then opened it.
- Rejected: the extra untracked-folder listing on every git, without the version check. It cost 10 ms more per save on macOS (two more git runs), for nothing on current git.
- Rejected: pathspecs (`--directory -- assets/`), which git 2.25 answers with nothing too.
- Known limit on old git only: an unreadable ignored folder inside an untracked folder still fails save, as before.
- A save no longer fails because a file is out of place. It saves and notes where things belong. It still refuses only what a save would lose (text or links in `assets/` or `out/`, a nested `.git`) and a changed saved asset.
- `agents.md` went from 798 to 538 words. The per-tool recipes (Manim, Motion Canvas, Hyperframes, Revideo) are gone, and the save notes teach placement when it matters.
- Support for older projects was dropped before any public release. Every `v2 init` gives the layout, in an empty or existing folder (existing files stay put and save notes them). Gone are `.v2/ignore`, the old ignore markers, `--changes`, `--preview` and the old commit.json forms (string changes, `preview`, `ffmpeg`). Projects made before v0.2.0 need a fresh `v2 init` in a new folder.
- Files in `assets/` can be replaced under the same name. The never-change rule only forced renames like `manim-intro-v2.mp4` and code edits. Each version records its own fingerprint and the store keeps every copy, so restore brings back each version's bytes either way.

## Adversarial sweep on v0.2.1 (2026-10-28)
- A media file outside `assets/` and `out/` with a line break in its name is refused. Its `.gitignore` line split in two. The media went into git, and the second half (e.g. `Video.tsx`) hid every text file with that name. `.gitignore` has no way to escape a line break, so refusing is the only exact option, and it keeps saves deterministic.
- Known and accepted: restore stops halfway when a path in `assets/` is a file in one version and a folder in the other. It needs a media file with no extension, which tools don't write. The fix would delete empty folders a framework may expect, which is too risky for a case that doesn't happen. To recover, save, then restore the latest version.
- Dropped: text matched by the user's own `.gitignore` isn't saved. The sweep wrote the matching line itself. Real starters ignore `node_modules`, `dist`, `.env` and `*.log`, which should stay out.

## Still open
- Time mapping across a restore.
- Codex is untested, including whether it reads AGENTS.md the same way.
- Re-rendering a deleted preview from `assemble`.
- Seeding the hash cache after restore (restored binaries are re-hashed once, about 4 s per 10 GB).

## Comment mode, and the page moves to React (2026-10-29)
- **Comments the agent acts on.** The page's comments left localStorage. They live in `.v2/comments/` (`comments.json` plus each comment's drawing as a PNG). `v2 comments` lists the open ones for the agent. `v2 save --resolves c1` marks c1 done in the new version, and the save after that deletes it. An unknown or closed ID refuses the save before anything changes.
- **Temporary, never in git.** `.v2/comments/` carries its own `.gitignore` (`*`), so comments never count as unsaved changes and never block a restore, even in projects whose ignore block predates them.
- **Design.** The home page lists versions newest first, with the prompt as the title, "From v3 to v4" change strips and a Comment mode button per version. Comment mode shows the video, a composer under it, timed cards on the right with a done tick, and dots on the scrub bar. Drawing on the frame is kept. Voice comments, labels and filters are left out. The prompt shows above the comments. Compare side by side is kept as it was.
- **No screen recording.** It was built, then removed before merge as too hard to get right.
- **Change kinds are only what the data knows.** A span is "Changed", a cut is "Cut", and no time is "Whole video".
- **A drawing is saved as the frame with the marks on it**, at the video's full size, so the agent sees exactly what was circled (`v2 comments` tells it to look).
- **React + Vite.** It is the same stack as v2.video, so the player and comment column can move to the site later. Measured at 240 KB JS (75 KB gzipped) against the old 30 KB page. The binary grows by about that, and only `v2 diff` ever reads it. `dist/` isn't committed, and the build steps say to build it first.
- Checked with a fresh `claude -p` in a copy of a test project. It found both comments, read the drawing correctly ("circles the gap between the title and the card") and planned `--resolves c1 --resolves c2`.

## The change timeline: "Paper" (2026-10-29, decided from six prototype rounds)
- **Direction:** under each version's video sits an editor-style timeline. It has a lane per kind (Text, Captions, Graphics, Video, Voice, Music, SFX, plus "All" for whole-video changes) and one block per change showing what it is, not frames. Text blocks show the quoted words, sound blocks show the real loudness of that stretch, and the rest show an icon and a few words. It is dark, on the page's own surface.
- **It follows the video.** The playhead and the blocks on screen light up as it plays. Click or drag the ruler or an empty track to scrub, and click a block to play from it. One line under the lanes carries the words (full sentence, file, time) for what's playing, or for what you point at when paused. Overlaps stack into "Text 2" like an editor. Cuts are red marks on the ruler.
- **Kind and file are logged by the agent.** `--change "0:02-0:09.8 voice assets/generated/vo-intro.mp3: voice-over for the intro"`. Kinds are text, captions, graphics, video, voice, music and sfx. Save refuses a file the project doesn't have. Lines without a kind and file still work and land in a "Changes" lane (older versions, cuts).
- **Loudness comes from the server** (`/peaks/vN`). ffmpeg decodes the render's sound at 8 kHz and Go keeps the loudest sample per 10 ms, about 24 KB a minute, cached. The browser never decodes a whole render.
- **Rejected along the way:** whole-render filmstrips and before/after frames (cluttered), stacked old-vs-new strips and side-by-side compare as the main view (too busy, so compare stays behind its button), text rows with colored sticks (no meaning without reading), a dial-in panel of presets (none looked right), and changes floating on the video or a rail of cards (no overview of the whole version).
- **The old "From v3 to v4" bar and list are gone,** and so is the poster-then-play card. Each version has a real video player the timeline follows.

## Cuts are a kind of change (2026-11-02)
- **A cut is just a kind of change.** `commit.json` keeps one `changes` list. A cut is `{"from": at, "to": at, "kind": "cut", "removed": seconds, "text": ...}` in the order the agent typed it. The `--change "cut 0:03.4 3.4s ..."` syntax is unchanged.
- **Old versions still read.** `commitAt` folds an old `cuts` list into `changes` as kind "cut", so `log`, `diff` and the page see one shape. Nothing writes `cuts` anymore. The page splits cuts back out in `web/src/lib.ts`.

## Added and re-timed changes (2026-11-02)
- **A change can say it's new or only re-timed.** `--change "0:12-0:15 added video assets/generated/end.mp4: final screen"` or `"0:04-0:09 retimed runs 1.2x faster"`. `commit.json` stores `"type": "added"` or `"retimed"`. Everything else has no type and means changed. The word only counts right after a time, so "added grain everywhere" stays a whole-video change, and cuts never get one.
- **Why:** v2.video shows Added and Re-timed labels, and v2 is the only CLI that feeds the site. This replaces the earlier rule that change kinds are only Changed, Cut and Whole video.

## Title and prompts are separate (2026-11-02)
- **A version's title is not the user's prompt.** In v2.video's older data the title is the agent's one-line summary ("V10: opens on Introducing v2, ... sound effects 20% quieter") and the prompts are the user's exact words, often dictated, 1 to 15 per version, each shown with the model. `-m` used to say "what was asked for (the prompt)", which blurred the two.
- **`-m` is now the title.** `--prompt` (repeatable, word for word, in order) and `--model` are new and optional. `commit.json` keeps the key `message` for the title, so v0.2.2 binaries still read it, and adds `prompts` and `model` only when given. No timestamp per prompt.
- `v2 log` prints them. The page's comment mode shows the prompts above the comments, falling back to the title.

## Push sends code as readable files (2026-11-05)
- Bundles keep a version's code, but the site can't open them, so pushed videos showed media only. `v2 push` now also sends each version's git-tracked files with `source: "code"`, skipping `.v2/`.
- Re-pushing fills in versions already on the site. Nothing else on them changes.
- At most 1000 files go up per version, because the video page reads every listed file. The rest stay in the bundle, and push says so.
- Code on a public video can be read by anyone. `.env` is ignored by default, but a key committed in a script would be public.
- Code and bundles upload 8 at a time. Three round trips per file took a real project's code 46 s one by one and 10 s together. 1100 files took 155 s.

## Site comments reach the agent (2026-11-05)
- `v2 comments` now also lists open comments from v2.video, using the site's ids.
- `--resolves` queues a site id in `.v2/comments/site-done.json`, and the next `v2 push` closes it. The fixing version isn't on the site until then, and a save has to work offline.
- A comment from anyone but the owner is flagged, so a public video's comments can't instruct the agent.
- `save` tells the agent to push when the project has a link, after a fresh agent left a fix unpushed.
- `save` also refreshes the v2 section of `AGENTS.md`, so updating the binary is enough.

## A save that changes no file drops nothing silently (2026-11-13)
- **The bug:** an agent saved its fix, then ran `v2 save ... --resolves <id>` only to close the comment. Save printed `nothing changed since the last save`, exited 0 and returned before it looked at `--resolves`, so the comment stayed open.
- **Now it closes the comment in the last version** and still makes no version. A resolve was never part of a version. It lives in `.v2/comments/` (`done_in`, or `site-done.json` for the next push), and the site keeps it on the comment (`doneIn`). So nothing saved or pushed is rewritten, and `commit.json` is unchanged.
- **A comment left on the last version is refused,** exit 1. No file changed since, so no version can hold its fix. The message says to make the fix, or to tell the user when it needs none. Offline, save can't learn which version a site comment was left on, so it refuses site ids then too, and says to save again when online and signed in. It asks the site only when a site id is named, so local ids work offline.
- **It prints the same push line as a save** when it queued a site comment and the project has a link, since site comments close only on the next push.
- **Comments done earlier are not cleared by it.** Only a new version does that, so the page keeps showing what the last version fixed.
- **`--render` and `--assemble` fail instead,** exit 1, when nothing changed and they differ from what the last version recorded. They belong in `commit.json`, which is never rewritten. The message says to set them in `.v2/config.json` and save again, which is a change and makes a version. Flags the last version already has are not an error, and both flags are named when both differ. A flag that only repeats `.v2/config.json` can't be fixed by setting it, so then the message says to save again without the flag.
- `restore` now records the restored version's render and assemble, so an agent that passes them as flags isn't refused right after a restore. It reads them before it changes the folder.
- **`-m`, `--prompt`, `--model` and `--change` are still dropped** with the plain message. They describe a new version, and there is none.
- **Rejected:** an empty new version per resolve (the site would show a version with no changes, and nothing changed means no tag). Also rejected was refusing every `--resolves` without changes, because the fix can be recorded honestly and the agent shouldn't have to fake an edit.

## Review fixes (2026-11-07)
- **Restore never writes through a link.** A cloned project could hold `src/cache -> ../.git` and list `src/cache/config` in an old version. Restore wrote it and git ran the planted `core.fsmonitor`. Restore now refuses any media path that goes through a link (nothing changes), and clone refuses links into `.git` or `.v2`.
- **`v2 diff` answers only its own page.** Requests must name `127.0.0.1` or `localhost`, because DNS rebinding sends the attacker's name. A write's `Origin` must be local and its `Sec-Fetch-Site` same-origin. Any local port is allowed, since the Vite dev proxy rewrites the host to `127.0.0.1:7777` but keeps `Origin: http://localhost:5173`. A website can't send a local name or origin, so this costs nothing against it. Checked in headless Chrome, the page's own comment and tick still work.
- **Network waits are bounded.** Convex calls time out after a minute. A part upload or download ends when no bytes move for 60 s, not after a total time, because a 200 MB part takes many minutes on a slow link. A rerun resumes as before. Not tested in e2e, since it would need a 60 s wait or a test-only setting.
- **Progress is one line per 10%** for copies, push and clone. Redrawing on every write printed 77 KB for a 100 MB push into agents' output.

## Distribution and self-update (2026-11-09)
- **Releases live in R2.** `scripts/release.sh` uploads five files to `cli/<version>/`. They are `v2-macos.zip`, `v2-linux-amd64`, `v2-linux-arm64`, `checksums.txt` and `checksums.txt.sig`. It needs docker, to check the Linux builds start. The site redirects `/cli/<version>/<file>` to a signed R2 link, and the server picks the current and oldest allowed version. A short stdlib updater (about 90 lines) and this shell script were picked over go-selfupdate and GoReleaser.
- **`v2 update` trusts a signature, not the server.** checksums.txt is signed with ed25519, and the key's public half is compiled in. A stolen storage or backend credential can serve any file, but not one that verifies. The new binary must also print the expected `--version` before it is renamed over the old one, so a failure at any step leaves the old binary as it was.
- **The test key goes in at link time.** e2e builds its old binary with `-X main.signingKey=<test key>`. A runtime override (an env var, even one honoured only by "dev" builds) would be code in every release that turns verification off. A link flag exists only in binaries built with it, and `release.sh` never passes it.
- **Every Convex call sends `version`.** The site refuses one below the oldest allowed version with the install line, and v2 prints that message as is. That error is how an old CLI learns it must update. There's no separate warning.
- **The update notice runs once a day, before the command,** with a 2 s timeout. It records the attempt even when it fails, so an offline day costs one short wait. `dev` builds skip it, since they have no release to compare with.

## Every version needs a prompt (2026-11-09)
- **Trigger:** a project's first two versions went up with no prompt. The agent followed an old local skill whose `v2 save` example had only `-m`, and save accepted it silently.
- **`v2 save` refuses without `--prompt`,** like it refuses without `--change`. The agent always has the user's message, and an error is the one thing agents reliably act on. `v2 restore` still saves without one, since there are no user words to copy.
- **No command to add a prompt after the fact.** `v2 prompt` and push's per-version warning were built, then removed. Once save refuses, only `v2 restore` makes a version without a prompt.
- **Save refuses what looks like a credential** in `--prompt`, `-m` or `--change` (OpenAI and Anthropic `sk-`, AWS, GitHub, Slack, Google, Replicate, Hugging Face, private keys, JWTs, `api_key=...`), and AGENTS.md tells the agent to write `[REDACTED]` instead. All three are pushed to v2.video, which can be public.

## Login when already signed in (2026-11-17)
- **Trigger:** agents following a tutorial prompt ran it against a test server. The prompt said "run v2 login, then v2 push", and `v2 login` printed a new code and waited for an approval even though the computer was signed in, so agents stalled for about two minutes each.
- **`v2 login` checks the saved login first.** If it is for the same server and `cli:whoami` still knows its token, login prints who is signed in and exits 0. A revoked token, a different `V2_SERVER`, or no answer from the site falls through to a normal login. Switching accounts is `v2 logout` then `v2 login`.
- **The login file is still one per computer,** not one per server, so a login to a test server replaces the prod one. Test scripts set `HOME` to a scratch folder.

## init refuses broad folders (2026-11-22)
- **Trigger:** agents sometimes run `v2 init` in the wrong folder, and in a home folder it starts tracking everything there.
- **No `V2_ROOT` setting** that limits v2 to one folder. It breaks v2 living inside an existing project (see "init leaves an existing repo alone"), and a setting in one shell protects nothing in the next one.
- **`init` refuses the folders that hold far more than one video.** These are `/`, `/Users`, `/home`, the temp folder, home, and its Desktop, Documents, Downloads, Movies, Music and Pictures. Folders inside them are fine. The same holds on Linux. A root user (home `/root`) is still refused in `/home`, and a desktop Linux has the same English folder names. The error says to make a folder and run init there, because agents act on errors. `clone` always makes a new folder, so it needs no check.
- **Agents are told too.** `v2 --help` and the AGENTS.md section say a new video gets its own folder. AGENTS.md arrives with init, so it helps an agent starting its next video. The refusal covers the first one.
