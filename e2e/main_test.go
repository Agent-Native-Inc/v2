package e2e

// End-to-end: build the real v2 binary once, then run it in throwaway folders.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var bin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "v2bin")
	if err = errors.Join(err,
		// Behave like a fresh machine: no global ignores or identity from the developer's git config.
		os.Setenv("GIT_CONFIG_GLOBAL", "/dev/null"),
		os.Setenv("GIT_CONFIG_NOSYSTEM", "1"),
		// Linux reads config from here before HOME, so a test's logout would delete the developer's real login.
		os.Unsetenv("XDG_CONFIG_HOME"),
	); err != nil {
		panic(err)
	}
	bin = filepath.Join(dir, "v2")
	goBuild(bin, "")
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// goBuild builds the v2 binary at path with -ldflags flags.
func goBuild(path, flags string) {
	args := []string{"build", "-ldflags", flags, "-o", path}
	if os.Getenv("GOCOVERDIR") != "" { // scripts/coverage.sh: the binary records which code each test ran
		args = append(args, "-cover", "-coverpkg=github.com/Agent-Native-Inc/v2/...")
	}
	if out, err := exec.Command("go", append(args, "../cmd/v2")...).CombinedOutput(); err != nil {
		panic(string(out))
	}
}

func TestInitTwiceIsSafe(t *testing.T) {
	dir := t.TempDir()
	for range 2 {
		mustRun(t, dir, nil, "init")
	}
	for _, p := range []string{".git", ".v2/objects"} {
		if fi, err := os.Stat(filepath.Join(dir, p)); err != nil || !fi.IsDir() {
			t.Fatalf("%s missing", p)
		}
	}
	if got := read(t, filepath.Join(dir, ".v2/assets.json")); got != "{}\n" {
		t.Fatalf("assets.json = %q", got)
	}
	gitignore := read(t, filepath.Join(dir, ".gitignore")) // a new project: the rules travel with it
	for _, l := range []string{".v2/objects/", ".v2/hashcache"} {
		if n := strings.Count(gitignore, l+"\n"); n != 1 {
			t.Fatalf("%q appears %d times in .gitignore, want 1", l, n)
		}
	}
}

func TestInitKeepsExistingRepo(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	put(t, dir, ".gitignore", "out/\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "alice's history")
	before := git(t, dir, "log", "--format=%H")

	mustRun(t, dir, nil, "init")
	if after := git(t, dir, "log", "--format=%H"); after != before {
		t.Fatal("init changed git history")
	}
	if got := read(t, filepath.Join(dir, ".gitignore")); !strings.HasPrefix(got, "out/\n") {
		t.Fatalf("init changed the user's .gitignore lines: %q", got)
	}
	// Binaries in the store must not show up as untracked files.
	put(t, dir, ".v2/objects/ab", "x")
	if st := git(t, dir, "status", "--porcelain"); strings.Contains(st, "objects") {
		t.Fatalf("store is visible to git:\n%s", st)
	}
}

// pathWith is a folder holding links to just these tools, for a PATH that lacks the rest.
func pathWith(t *testing.T, tools ...string) string {
	t.Helper()
	bare := t.TempDir()
	for _, tool := range tools {
		if p, err := exec.LookPath(tool); err == nil {
			must(t, os.Symlink(p, filepath.Join(bare, tool)))
		}
	}
	return bare
}

// installHint is what v2 should tell the user here: the Linux test images are Debian, which has apt.
func installHint(pkg string) string {
	if runtime.GOOS == "darwin" {
		return map[string]string{"git": "`xcode-select --install`", "ffmpeg": "`brew install ffmpeg`"}[pkg]
	}
	if os.Geteuid() == 0 {
		return "`apt install " + pkg + "`"
	}
	return "`sudo apt install " + pkg + "`"
}

// needsApt skips a test that expects apt's install hints, on a Linux without apt (Fedora has dnf).
func needsApt(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("apt"); runtime.GOOS == "linux" && err != nil {
		t.Skip("expects apt's install hints, and this Linux has no apt")
	}
}

func TestNoGitThroughCLI(t *testing.T) {
	needsApt(t)
	out, code := v2(t, t.TempDir(), []string{"PATH=" + pathWith(t, "ffmpeg", "ffprobe", "apt")}, "init")
	if code != 1 || !strings.Contains(out, installHint("git")) {
		t.Fatalf("exit %d: %s", code, out)
	}
	if runtime.GOOS == "linux" {
		out, code := v2(t, t.TempDir(), []string{"PATH=" + pathWith(t, "ffmpeg", "ffprobe")}, "init")
		if code != 1 || !strings.Contains(out, "Install git with your package manager") {
			t.Fatalf("with no package manager: exit %d: %s", code, out)
		}
	}
}

func TestUnknownCommandExits1(t *testing.T) {
	if _, code := v2(t, t.TempDir(), nil, "frobnicate"); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
}

func TestInitTeachesAgents(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, "AGENTS.md", "# Alice's rules\nuse tabs")
	for range 2 {
		mustRun(t, dir, nil, "init")
	}
	agents := read(t, filepath.Join(dir, "AGENTS.md"))
	if !strings.HasPrefix(agents, "# Alice's rules\nuse tabs\n") {
		t.Fatalf("init changed the user's AGENTS.md:\n%s", agents)
	}
	if strings.Count(agents, "<!-- v2: start -->") != 1 || !strings.Contains(agents, "v2 save -m") {
		t.Fatalf("v2 section missing or duplicated:\n%s", agents)
	}
	if got := read(t, filepath.Join(dir, "CLAUDE.md")); got != "@AGENTS.md\n" {
		t.Fatalf("CLAUDE.md = %q", got)
	}

	// After an upgrade, init again replaces an old v2 section and keeps the user's text around it.
	old := "# Alice's rules\n<!-- v2: start -->\nuse --changes and preview\n<!-- v2: end -->\nmy notes after\n"
	put(t, dir, "AGENTS.md", old)
	mustRun(t, dir, nil, "init")
	agents = read(t, filepath.Join(dir, "AGENTS.md"))
	if strings.Contains(agents, "--changes") || !strings.Contains(agents, "--change \"0:04-0:09") ||
		!strings.HasPrefix(agents, "# Alice's rules\n<!-- v2: start -->") || !strings.HasSuffix(agents, "<!-- v2: end -->\nmy notes after\n") {
		t.Fatalf("old section not replaced cleanly:\n%s", agents)
	}
}

func TestNeedsFFmpeg(t *testing.T) {
	needsApt(t)
	bare := pathWith(t, "git", "apt")
	dir := t.TempDir()
	for _, args := range [][]string{{"init"}, {"diff", "--no-open"}} {
		out, code := v2(t, dir, []string{"PATH=" + bare}, args...)
		if code != 1 || !strings.Contains(out, installHint("ffmpeg")) {
			t.Fatalf("%v without ffmpeg: exit %d: %s", args, code, out)
		}
	}
}

// Fedora's own ffmpeg (ffmpeg-free) makes push's 1080p copy, so dnf users get the plain command.
func TestNeedsFFmpegWithDnf(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("dnf is Linux only")
	}
	fedora := pathWith(t, "git")
	must(t, os.WriteFile(filepath.Join(fedora, "dnf"), []byte("#!/bin/sh\n"), 0o755))
	want := strings.ReplaceAll(installHint("ffmpeg"), "apt", "dnf") + ", then try again"
	if out, code := v2(t, t.TempDir(), []string{"PATH=" + fedora}, "init"); code != 1 || !strings.Contains(out, want) {
		t.Fatalf("want %q: exit %d: %s", want, code, out)
	}
}
