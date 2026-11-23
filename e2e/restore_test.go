package e2e

import (
	"crypto/sha256"
	"encoding/hex"
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
			sum := sha256.Sum256(b)
			m[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
		}
		return nil
	}))
	return m
}

func restore(t *testing.T, dir, v string) string {
	t.Helper()
	out, code := v2(t, dir, gitIdentity, "restore", v)
	if code != 0 {
		t.Fatalf("restore %s: exit %d: %s", v, code, out)
	}
	return out
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
	must(t, os.MkdirAll(filepath.Join(dir, "src"), 0o755))
	must(t, os.MkdirAll(filepath.Join(dir, "out"), 0o755))
	writeRandom(t, filepath.Join(dir, "out/clip.mov"), 1<<20)
	must(t, os.WriteFile(filepath.Join(dir, "src/Video.tsx"), []byte("0:04\n"), 0o644))
	save(t, dir, "-m", "v1", "--change", "first cut")
	v1 := snapshot(t, dir)

	writeRandom(t, filepath.Join(dir, "out/final.mp4"), 1<<20)
	must(t, os.WriteFile(filepath.Join(dir, "src/captions.srt"), []byte("hi\n"), 0o644))
	save(t, dir, "-m", "v2", "--change", "render + captions")
	v2s := snapshot(t, dir)

	writeRandom(t, filepath.Join(dir, "out/clip.mov"), 1<<20)
	must(t, os.WriteFile(filepath.Join(dir, "src/Video.tsx"), []byte("0:06\n"), 0o644))
	must(t, os.Remove(filepath.Join(dir, "out/final.mp4")))
	must(t, os.Remove(filepath.Join(dir, "src/captions.srt")))
	save(t, dir, "-m", "v3", "--change", "new clip, longer intro")
	return dir, v1, v2s, snapshot(t, dir)
}

// Risk 1: files from later versions linger (or are missing) after restore,
// and restoring an old version must not make newer ones unrestorable.
func TestRestoreExactAndForwardOnly(t *testing.T) {
	dir, v1, v2s, v3 := threeVersions(t)

	out := restore(t, dir, "v1")
	if !strings.Contains(out, "restored v1 (saved as v4)") || strings.Contains(out, "auto-save") {
		t.Fatalf("output: %s", out)
	}
	sameFiles(t, "after restore v1", snapshot(t, dir), v1)
	if st := git(t, dir, "status", "--porcelain"); st != "" {
		t.Fatalf("not clean after restore:\n%s", st)
	}

	sameFiles(t, "then restore v3", func() map[string]string { restore(t, dir, "v3"); return snapshot(t, dir) }(), v3)
	sameFiles(t, "then restore v2", func() map[string]string { restore(t, dir, "v2"); return snapshot(t, dir) }(), v2s)
	// v2 has captions.srt and final.mp4, v1 has neither: both must go.
	sameFiles(t, "then v1 again", func() map[string]string { restore(t, dir, "v1"); return snapshot(t, dir) }(), v1)
	if tags := git(t, dir, "tag", "--list"); strings.Count(tags, "\n") != 7 {
		t.Fatalf("want v1..v7, got:\n%s", tags)
	}
}

// Risk 2: unsaved work is overwritten by restore. Restore must refuse, say what's unsaved, and touch nothing.
func TestRestoreRefusesUnsavedWork(t *testing.T) {
	cases := map[string]func(t *testing.T, dir string){
		"edited code": func(t *testing.T, dir string) {
			t.Helper()
			must(t, os.WriteFile(filepath.Join(dir, "src/Video.tsx"), []byte("unsaved idea\n"), 0o644))
		},
		"new text file": func(t *testing.T, dir string) {
			t.Helper()
			must(t, os.WriteFile(filepath.Join(dir, "src/notes.md"), []byte("idea\n"), 0o644))
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

			out, code := v2(t, dir, gitIdentity, "restore", "v1")
			if code != 1 || !strings.Contains(out, "unsaved") || !strings.Contains(out, "v2 save") {
				t.Fatalf("want refusal, got exit %d: %s", code, out)
			}
			sameFiles(t, "folder after refused restore", snapshot(t, dir), before)
			if tags := git(t, dir, "tag", "--list"); tags != "v1\nv2\nv3\n" {
				t.Fatalf("refused restore made a version:\n%s", tags)
			}

			// After saving, the restore goes through and the saved work stays restorable.
			save(t, dir, "-m", "keep it", "--change", name)
			saved := snapshot(t, dir) // save updates .gitignore's list of binaries
			restore(t, dir, "v1")
			sameFiles(t, "after save + restore v1", snapshot(t, dir), v1)
			restore(t, dir, "v4")
			sameFiles(t, "saved work back from v4", snapshot(t, dir), saved)
		})
	}
}

// Risk 3: a corrupted or missing stored object puts bad bytes in the folder, or leaves it half-restored.
func TestRestoreCorruptObjectChangesNothing(t *testing.T) {
	dir, _, _, _ := threeVersions(t)
	// v4 adds a binary, so a restore to v1 has something to delete and a recipe file to change.
	writeRandom(t, filepath.Join(dir, "out/extra.mp4"), 1<<20)
	save(t, dir, "-m", "v4", "--change", "extra")
	v4 := snapshot(t, dir)
	var a1 map[string]asset
	if err := jsonUnmarshal(git(t, dir, "show", "v1:.v2/assets.json"), &a1); err != nil {
		t.Fatal(err)
	}
	sha := a1["out/clip.mov"].Sha256
	obj := filepath.Join(dir, ".v2/objects", sha[:2], sha[2:])
	must(t, os.Chmod(obj, 0o644))
	must(t, os.WriteFile(obj, []byte("bit rot"), 0o644))

	out, code := v2(t, dir, gitIdentity, "restore", "v1")
	if code != 1 || !strings.Contains(out, "nothing was changed") || !strings.Contains(out, "corrupted") {
		t.Fatalf("exit %d: %s", code, out)
	}
	sameFiles(t, "folder after failed restore", snapshot(t, dir), v4)
	if st := git(t, dir, "status", "--porcelain"); st != "" {
		t.Fatalf("failed restore left changes:\n%s", st)
	}
}

func TestRestoreUnknownVersion(t *testing.T) {
	dir, _, _, _ := threeVersions(t)
	if out, code := v2(t, dir, gitIdentity, "restore", "v9"); code != 1 || !strings.Contains(out, "v2 log") {
		t.Fatalf("exit %d: %s", code, out)
	}
}

func TestNodeModulesNeverCommittedOnFirstSave(t *testing.T) {
	dir := t.TempDir()
	newProject(t, dir)
	must(t, os.MkdirAll(filepath.Join(dir, "node_modules/x"), 0o755))
	must(t, os.WriteFile(filepath.Join(dir, "node_modules/x/index.js"), []byte("1\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(dir, "src/Video.tsx"), []byte("a\n"), 0o644))
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
	must(t, os.MkdirAll(filepath.Join(dir, "src"), 0o755))
	must(t, os.WriteFile(filepath.Join(dir, "src/Video.tsx"), []byte("a\n"), 0o644))
	save(t, dir, "-m", "v1", "--change", "x")

	appendTo(t, filepath.Join(dir, ".gitignore"), ".env\n.secrets/\nsecret/\n")
	must(t, os.WriteFile(filepath.Join(dir, ".env"), []byte("ELEVENLABS_API_KEY=sk-123\n"), 0o644))
	must(t, os.MkdirAll(filepath.Join(dir, ".secrets"), 0o755))
	must(t, os.WriteFile(filepath.Join(dir, ".secrets/openai.txt"), []byte("sk-456\n"), 0o644))
	must(t, os.MkdirAll(filepath.Join(dir, "tools/x/secret"), 0o755))
	must(t, os.WriteFile(filepath.Join(dir, "tools/x/secret/key.txt"), []byte("sk-789\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(dir, "src/Video.tsx"), []byte("b\n"), 0o644))
	save(t, dir, "-m", "v2", "--change", "x")

	restore(t, dir, "v1")
	if log := git(t, dir, "log", "--all", "--oneline", "--", ".env", ".secrets", "tools"); log != "" {
		t.Fatalf("restore committed ignored secrets:\n%s", log)
	}
	restore(t, dir, "v2")
	for _, p := range []string{".env", ".secrets/openai.txt", "tools/x/secret/key.txt"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
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
	h := sum(evil)
	put(t, dir, ".v2/objects/"+h[:2]+"/"+h[2:], string(evil))
	put(t, dir, ".v2/assets.json", `{"src/cache/config":{"sha256":"`+h+`","size":1,"source":"derived"}}`)
	retag(t, dir)
	put(t, dir, "src/a.txt", "b")
	save(t, dir, "-m", "two", "--change", "two")

	out, code := v2(t, dir, gitIdentity, "restore", "v1")
	if code != 1 || !strings.Contains(out, "src/cache/config") {
		t.Fatalf("exit %d: %s", code, out)
	}
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
	put(t, dir, filepath.Join(".v2/objects", sum(meta)[:2], sum(meta)[2:]), string(meta))
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
