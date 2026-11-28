package e2e

import (
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// snapshot hashes every project file outside .git and .v2: what Alice would see in Finder.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	m := map[string]string{}
	must(t, filepath.WalkDir(dir, func(p string, d fs.DirEntry, _ error) error {
		rel, _ := filepath.Rel(dir, p)
		if d.IsDir() && (rel == ".git" || rel == ".v2") {
			return filepath.SkipDir
		}
		if !d.IsDir() {
			b, _ := os.ReadFile(p)
			m[filepath.ToSlash(rel)] = sum(b)
		}
		return nil
	}))
	return m
}

func sameFiles(t *testing.T, label string, got, want map[string]string) {
	t.Helper()
	if !maps.Equal(got, want) {
		t.Fatalf("%s:\n got  %v\n want %v", label, got, want)
	}
}

// Three versions with every kind of change between them: edited, added and deleted
// text files, and added, replaced and deleted binaries.
func threeVersions(t *testing.T) (string, map[string]string, map[string]string, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	newProject(t, dir)
	writeRandom(t, filepath.Join(dir, "out/clip.mov"), 1<<20)
	put(t, dir, "src/Video.tsx", "0:04\n")
	save(t, dir, "-m", "v1", "--change", "first cut")
	v1 := snapshot(t, dir)

	writeRandom(t, filepath.Join(dir, "out/final.mp4"), 1<<20)
	put(t, dir, "src/captions.srt", "hi\n")
	save(t, dir, "-m", "v2", "--change", "render + captions")
	v2s := snapshot(t, dir)

	writeRandom(t, filepath.Join(dir, "out/clip.mov"), 1<<20)
	put(t, dir, "src/Video.tsx", "0:06\n")
	must(t, os.Remove(filepath.Join(dir, "out/final.mp4")))
	must(t, os.Remove(filepath.Join(dir, "src/captions.srt")))
	save(t, dir, "-m", "v3", "--change", "new clip, longer intro")
	return dir, v1, v2s, snapshot(t, dir)
}

// Restore leaves no file from a later version and misses none, and restoring an old version must not make newer ones unrestorable.
func TestRestoreExactAndForwardOnly(t *testing.T) {
	dir, v1, v2s, v3 := threeVersions(t)

	out := restore(t, dir, "v1")
	if !strings.Contains(out, "restored v1 (saved as v4)") || strings.Contains(out, "auto-save") {
		t.Fatalf("output: %s", out)
	}
	sameFiles(t, "after restore v1", snapshot(t, dir), v1)
	clean(t, dir)

	// v2 has captions.srt and final.mp4, v1 has neither: both must go.
	for _, c := range []struct {
		v    string
		want map[string]string
	}{{"v3", v3}, {"v2", v2s}, {"v1", v1}} {
		restore(t, dir, c.v)
		sameFiles(t, "then restore "+c.v, snapshot(t, dir), c.want)
	}
	if tags := git(t, dir, "tag", "--list"); strings.Count(tags, "\n") != 7 {
		t.Fatalf("want v1..v7, got:\n%s", tags)
	}
}

// Restore never overwrites unsaved work: it must refuse, say what's unsaved, and touch nothing.
func TestRestoreRefusesUnsavedWork(t *testing.T) {
	cases := map[string]func(t *testing.T, dir string){
		"edited code": func(t *testing.T, dir string) {
			t.Helper()
			put(t, dir, "src/Video.tsx", "unsaved idea\n")
		},
		"new text file": func(t *testing.T, dir string) {
			t.Helper()
			put(t, dir, "src/notes.md", "idea\n")
		},
		"new render": func(t *testing.T, dir string) {
			t.Helper()
			writeRandom(t, filepath.Join(dir, "out/new-render.mp4"), 1<<20)
		},
		"replaced clip": func(t *testing.T, dir string) {
			t.Helper()
			writeRandom(t, filepath.Join(dir, "out/clip.mov"), 1<<20)
		},
		"deleted clip": func(t *testing.T, dir string) {
			t.Helper()
			must(t, os.Remove(filepath.Join(dir, "out/clip.mov")))
		},
		"deleted script": func(t *testing.T, dir string) {
			t.Helper()
			must(t, os.Remove(filepath.Join(dir, "src/Video.tsx")))
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			dir, v1, _, _ := threeVersions(t)
			change(t, dir)
			before := snapshot(t, dir)

			refused(t, dir, []string{"restore", "v1"}, "unsaved", "v2 save", "--prompt")
			sameFiles(t, "folder after refused restore", snapshot(t, dir), before)

			// After saving, the restore goes through and the saved work stays restorable.
			mustRun(t, dir, gitIdentity, "save", "-m", "keep it", "--prompt", "keep these edits", "--change", name)
			saved := snapshot(t, dir) // save updates .gitignore's list of binaries
			restore(t, dir, "v1")
			sameFiles(t, "after save + restore v1", snapshot(t, dir), v1)
			restore(t, dir, "v4")
			sameFiles(t, "saved work back from v4", snapshot(t, dir), saved)
		})
	}
}

// A corrupted or missing stored object must not put bad bytes in the folder or leave it half-restored.
func TestRestoreCorruptObjectChangesNothing(t *testing.T) {
	dir, _, _, _ := threeVersions(t)
	// v4 adds a binary, so a restore to v1 has something to delete and a recipe file to change.
	writeRandom(t, filepath.Join(dir, "out/extra.mp4"), 1<<20)
	save(t, dir, "-m", "v4", "--change", "extra")
	v4 := snapshot(t, dir)
	obj := objectPath(dir, assetsAt(t, dir, "v1")["out/clip.mov"].Sha256)
	must(t, os.Chmod(obj, 0o644))
	must(t, os.WriteFile(obj, []byte("bit rot"), 0o644))

	refused(t, dir, []string{"restore", "v1"}, "nothing was changed", "corrupted")
	sameFiles(t, "folder after failed restore", snapshot(t, dir), v4)
	clean(t, dir)
}

func TestRestoreUnknownVersion(t *testing.T) {
	dir, _, _, _ := threeVersions(t)
	refused(t, dir, []string{"restore", "v9"}, "v2 log")
}

func TestNodeModulesNeverCommittedOnFirstSave(t *testing.T) {
	dir := t.TempDir()
	newProject(t, dir)
	put(t, dir, "node_modules/x/index.js", "1\n")
	put(t, dir, "src/Video.tsx", "a\n")
	if st := git(t, dir, "status", "--porcelain", "--untracked-files=all"); strings.Contains(st, "node_modules") {
		t.Fatalf("git sees node_modules right after init:\n%s", st)
	}
	save(t, dir, "-m", "v1", "--change", "x")
	if files := git(t, dir, "ls-files"); strings.Contains(files, "node_modules") {
		t.Fatalf("node_modules went into git:\n%s", files)
	}
}

// An API key file gitignored after v1 must not be committed by restoring v1 (whose .gitignore
// doesn't list it), nor deleted by restoring forward again. tools/x/secret/ sits in a folder git
// doesn't track, where git 2.30 and older (Ubuntu 20.04) didn't report it.
func TestRestoreKeepsIgnoredFilesOutOfGit(t *testing.T) {
	dir := t.TempDir()
	newProject(t, dir)
	put(t, dir, "src/Video.tsx", "a\n")
	save(t, dir, "-m", "v1", "--change", "x")

	appendTo(t, filepath.Join(dir, ".gitignore"), ".env\n.secrets/\nsecret/\n")
	put(t, dir, ".env", "ELEVENLABS_API_KEY=sk-123\n")
	put(t, dir, ".secrets/openai.txt", "sk-456\n")
	put(t, dir, "tools/x/secret/key.txt", "sk-789\n")
	put(t, dir, "src/Video.tsx", "b\n")
	save(t, dir, "-m", "v2", "--change", "x")

	restore(t, dir, "v1")
	if log := git(t, dir, "log", "--all", "--oneline", "--", ".env", ".secrets", "tools"); log != "" {
		t.Fatalf("restore committed ignored secrets:\n%s", log)
	}
	restore(t, dir, "v2")
	for _, p := range []string{".env", ".secrets/openai.txt", "tools/x/secret/key.txt"} {
		if !exists(filepath.Join(dir, p)) {
			t.Fatalf("%s is gone from disk after restoring forward", p)
		}
	}
}

// Restoring to before a Remotion subproject leaves its ignored node_modules in a folder git
// doesn't track. Restoring forward keeps that folder ignored with one line, not a line per file,
// which on git 2.30 and older (Ubuntu 20.04) grew to 80,000 lines and slowed every save.
func TestRestoreKeepsIgnoredFolderAsOneLine(t *testing.T) {
	dir := t.TempDir()
	newProject(t, dir)
	put(t, dir, "src/Video.tsx", "a\n")
	save(t, dir, "-m", "v1", "--change", "x")
	put(t, dir, "remotion/package.json", "{}\n")
	for _, f := range []string{"react/index.js", "react/cjs/react.js", "zod/index.js"} {
		put(t, dir, "remotion/node_modules/"+f, "x\n")
	}
	save(t, dir, "-m", "v2", "--change", "x")

	restore(t, dir, "v1")
	restore(t, dir, "v2")
	exclude := read(t, filepath.Join(dir, ".git/info/exclude"))
	if strings.Contains(exclude, ".js") || !strings.Contains(exclude, "\n/remotion/node_modules/\n") {
		t.Fatalf("node_modules isn't one line in .git/info/exclude:\n%s", exclude)
	}
}

// A version where an asset sits behind a link into .git, as a stranger's cloned project could have.
func TestRestoreNeverWritesThroughALink(t *testing.T) {
	dir := t.TempDir()
	newProject(t, dir)
	pwned := filepath.Join(t.TempDir(), "PWNED")
	evil := []byte("[core]\n\trepositoryformatversion = 0\n\tbare = false\n\tfsmonitor = \"touch " + pwned + "; true\"\n")
	must(t, os.Symlink("../.git", filepath.Join(dir, "src", "cache")))
	put(t, dir, "src/a.txt", "a")
	save(t, dir, "-m", "one", "--change", "one")
	h := storeObject(t, dir, evil)
	put(t, dir, ".v2/assets.json", `{"src/cache/config":{"sha256":"`+h+`","size":1,"source":"derived"}}`)
	retag(t, dir)
	put(t, dir, "src/a.txt", "b")
	save(t, dir, "-m", "two", "--change", "two")

	refused(t, dir, []string{"restore", "v1"}, "src/cache/config")
	if strings.Contains(read(t, filepath.Join(dir, ".git/config")), "fsmonitor") || exists(pwned) {
		t.Fatal("restore wrote into .git through a link")
	}
}

// An agent's prebuilt tool must still run after a restore, and a private file must stay private.
func TestRestoreKeepsPermissions(t *testing.T) {
	dir := t.TempDir()
	newProject(t, dir)
	tool, private := filepath.Join(dir, "scripts/bin/tool"), filepath.Join(dir, "assets/generated/private.mp4")
	put(t, dir, "scripts/bin/tool", "\x7fELF\x00 prebuilt") // the NUL makes it a binary, so it goes through the store
	writeRandom(t, private, 1000)
	must(t, os.Chmod(tool, 0o755))
	must(t, os.Chmod(private, 0o600))
	save(t, dir, "-m", "one", "--change", "one")
	must(t, os.Remove(tool))
	must(t, os.Remove(private))
	save(t, dir, "-m", "two", "--change", "two")
	restore(t, dir, "v1")
	for path, want := range map[string]os.FileMode{tool: 0o755, private: 0o600} {
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != want {
			t.Fatalf("%s: %v, want %v (%v)", path, fi.Mode().Perm(), want, err)
		}
	}
}

// Where files can't be cloned, restore copies a video out of the store, and its save must not copy it
// back in to hash it again: a 700 MB render would write 1.4 GB and show two copies.
func TestRestoreCopiesOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("mounts a disk image")
	}
	dir := filepath.Join(mountDisk(t, noClones), "proj")
	must(t, os.Mkdir(dir, 0o755))
	newProject(t, dir)
	writeRandom(t, filepath.Join(dir, "out/final.mp4"), 100<<20)
	save(t, dir, "-m", "one", "--change", "one")
	must(t, os.Remove(filepath.Join(dir, "out/final.mp4")))
	save(t, dir, "-m", "two", "--change", "two")
	if out := restore(t, dir, "v1"); strings.Count(out, "copying") != 1 {
		t.Fatalf("want one copy:\n%s", out)
	}
}

// savedWithAppleDouble saves v1 in dir the way v2 did before it skipped the Mac's ._ files: with
// out/._final.mp4 listed as media and in the store, .v2/._assets.json tracked in git (on exFAT the Mac
// writes one for v2's own records), and a .gitignore that doesn't name them.
func savedWithAppleDouble(t *testing.T, dir string) ([]byte, []byte) {
	t.Helper()
	clip := writeRandom(t, filepath.Join(dir, "out/final.mp4"), 1<<10)
	put(t, dir, "src/Video.tsx", "0:04\n")
	save(t, dir, "-m", "one", "--change", "one")
	meta := writeRandom(t, filepath.Join(dir, "out/._final.mp4"), 1<<10)
	storeObject(t, dir, meta)
	listed := assets(t, dir)
	listed["out/._final.mp4"] = asset{Sha256: sum(meta), Source: "derived", Size: int64(len(meta))}
	put(t, dir, ".v2/assets.json", mustJSON(t, listed))
	put(t, dir, ".gitignore", strings.Replace(read(t, filepath.Join(dir, ".gitignore")), "._*\n", "", 1))
	put(t, dir, ".v2/._assets.json", "Mac metadata\n")
	git(t, dir, "add", "-f", ".v2/._assets.json")
	git(t, dir, "commit", "-qa", "--amend", "--no-edit")
	git(t, dir, "tag", "-f", "v1")
	return clip, meta
}

// A version saved before v2 skipped the Mac's ._ files lists them as media, and its .gitignore doesn't
// name them. Restoring it brings back the real files only: on exFAT the Mac moves and deletes ._ files
// itself, so placing one too could stop the restore halfway.
func TestRestoreVersionSavedWithAppleDouble(t *testing.T) {
	dir := t.TempDir()
	newProject(t, dir)
	clip, _ := savedWithAppleDouble(t, dir)
	put(t, dir, "._d/f", "in a ._ folder\n")
	git(t, dir, "add", "-f", "._d/f")
	git(t, dir, "commit", "-q", "--amend", "--no-edit")
	git(t, dir, "tag", "-f", "v1")
	must(t, os.RemoveAll(filepath.Join(dir, "._d")))

	put(t, dir, "src/._Video.tsx", "Mac metadata\n")
	restore(t, dir, "v1")

	writeRandom(t, filepath.Join(dir, "out/final.mp4"), 1<<10)
	save(t, dir, "-m", "two", "--change", "two")
	put(t, dir, "out/._final.mp4", "the Mac's own")
	restore(t, dir, "v1")
	if read(t, filepath.Join(dir, "out/final.mp4")) != string(clip) {
		t.Fatal("out/final.mp4 is not v1's")
	}
	if got := read(t, filepath.Join(dir, "out/._final.mp4")); got != "the Mac's own" {
		t.Fatalf("restore replaced out/._final.mp4 with %d bytes from v1", len(got))
	}
	if exists(filepath.Join(dir, "._d")) {
		t.Fatal("restore wrote ._d/f")
	}
}

// A release binary (V2_OLD_BIN) and this one take turns on one project: each reads, lists and
// restores what the other saved.
func TestReleaseBinaryAndThisOneShareAProject(t *testing.T) {
	old := os.Getenv("V2_OLD_BIN")
	if old == "" {
		t.Skip("set V2_OLD_BIN to a release binary")
	}
	oldRun := func(dir string, args ...string) string {
		t.Helper()
		out, code := runBin(t, old, dir, gitIdentity, args...)
		if code != 0 {
			t.Fatalf("old %v: exit %d: %s", args, code, out)
		}
		return out
	}
	dir := t.TempDir()
	oldRun(dir, "init")
	clip := writeRandom(t, filepath.Join(dir, "assets/clip.mov"), 1<<20)
	put(t, dir, "src/edit.txt", "old one")
	oldRun(dir, "save", "-m", "old one", "--prompt", "make it", "--change", "first cut")
	put(t, dir, "src/edit.txt", "new two")
	must(t, os.Remove(filepath.Join(dir, "assets/clip.mov")))
	save(t, dir, "-m", "new two", "--change", "clip removed")
	put(t, dir, "src/edit.txt", "old three")
	oldRun(dir, "save", "-m", "old three", "--prompt", "again", "--change", "new text")

	for name, log := range map[string]string{"old": oldRun(dir, "log"), "new": mustRun(t, dir, nil, "log")} {
		for _, m := range []string{"old one", "new two", "old three"} {
			if !strings.Contains(log, m) {
				t.Fatalf("%s log misses %q:\n%s", name, m, log)
			}
		}
	}
	restore(t, dir, "v1") // new restores what old saved
	if read(t, filepath.Join(dir, "src/edit.txt")) != "old one" || read(t, filepath.Join(dir, "assets/clip.mov")) != string(clip) {
		t.Fatal("new restore v1 didn't bring back old's files")
	}
	oldRun(dir, "restore", "v2") // old restores what new saved
	if read(t, filepath.Join(dir, "src/edit.txt")) != "new two" || exists(filepath.Join(dir, "assets/clip.mov")) {
		t.Fatal("old restore v2 didn't bring back new's files")
	}
}
