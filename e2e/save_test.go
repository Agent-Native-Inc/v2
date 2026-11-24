package e2e

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestThreeSaves(t *testing.T) {
	proj, footage := t.TempDir(), t.TempDir()
	newProject(t, proj)
	put(t, proj, ".v2/config.json", `{"assemble": "npx remotion render Main out/final.mp4"}`)
	writeRandom(t, filepath.Join(footage, "clip.mov"), 1<<20)
	mustRun(t, proj, nil, "import", filepath.Join(footage, "clip.mov"))
	put(t, proj, "src/Video.tsx", "cut at 0:04\n")

	// v1: imported clip + code.
	save(t, proj, "-m", "first cut", "--change", "0:00-0:04 intro")
	// v2: the agent renders a derived file. --assemble overrides the config for this save only.
	writeRandom(t, filepath.Join(proj, "out/final.mp4"), 1<<20)
	save(t, proj, "-m", "render", "--change", "rendered", "--assemble", "ffmpeg -i x final.mp4")
	// Nothing changed: no new version.
	if out := save(t, proj, "-m", "again", "--change", "nothing"); !strings.Contains(out, "nothing changed") {
		t.Fatalf("empty save: %s", out)
	}
	// v3: code edit, render deleted.
	put(t, proj, "src/Video.tsx", "cut at 0:06\n")
	must(t, os.Remove(filepath.Join(proj, "out/final.mp4")))
	save(t, proj, "-m", "longer intro", "--change", "0:04 -> 0:06")

	if tags := git(t, proj, "tag", "--list"); tags != "v1\nv2\nv3\n" {
		t.Fatalf("tags = %q", tags)
	}
	stat := git(t, proj, "log", "--stat", "--format=")
	for _, bin := range []string{"clip.mov", "final.mp4"} {
		if strings.Contains(stat, bin) {
			t.Fatalf("binary %s went into git:\n%s", bin, stat)
		}
	}
	if !strings.Contains(stat, "src/Video.tsx") {
		t.Fatalf("code missing from git:\n%s", stat)
	}

	// Each version's assets.json lists exactly that version's binaries, with the right source.
	a2 := assetsAt(t, proj, "v2")
	if a2["assets/imported/clip.mov"].Source != "imported" || a2["out/final.mp4"].Source != "derived" {
		t.Fatalf("v2 assets = %+v", a2)
	}
	a3 := assetsAt(t, proj, "v3")
	if _, ok := a3["out/final.mp4"]; ok || len(a3) != 1 {
		t.Fatalf("v3 assets = %+v", a3)
	}
	// Every binary any version needs is in the store.
	for _, a := range []asset{a2["assets/imported/clip.mov"], a2["out/final.mp4"]} {
		if !exists(objectPath(proj, a.Sha256)) {
			t.Fatalf("object %s missing", a.Sha256)
		}
	}

	if c := commitAt(t, proj, "v1"); c["assemble"] != "npx remotion render Main out/final.mp4" || fmt.Sprint(c["changes"]) != "[map[from:0 text:intro to:4]]" {
		t.Fatalf("v1 commit.json = %v", c)
	}
	if c := commitAt(t, proj, "v2"); c["assemble"] != "ffmpeg -i x final.mp4" {
		t.Fatalf("v2 should use the --assemble override: %v", c)
	}
	if c := commitAt(t, proj, "v3"); c["assemble"] != "npx remotion render Main out/final.mp4" {
		t.Fatalf("v3 should be back to the config: %v", c)
	}
	// Deleting final.mp4 must un-hide its path, or a future text file there would be invisible to git.
	if ex := read(t, filepath.Join(proj, ".git/info/exclude")); strings.Contains(ex, "final.mp4") {
		t.Fatalf("stale exclude entry:\n%s", ex)
	}
	clean(t, proj)
}

func TestSaveNeedsMessageAndChanges(t *testing.T) {
	proj := t.TempDir()
	newProject(t, proj)
	for _, args := range [][]string{{"save", "--prompt", "p"}, {"save", "--prompt", "p", "-m", "x"}, {"save", "--prompt", "p", "--change", "y"}} {
		refused(t, proj, args, "--change")
	}
}

func TestSaveWeirdFileNames(t *testing.T) {
	proj := t.TempDir()
	newProject(t, proj)
	for _, name := range []string{"assets/imported/take [1].mp4", "assets/imported/a*b?.mov", "assets/imported/trailing .wav "} {
		writeRandom(t, filepath.Join(proj, name), 1<<20) // big enough to contain a NUL, like real media
	}
	save(t, proj, "-m", "x", "--change", "y")
	if st := git(t, proj, "status", "--porcelain", "--ignored"); strings.Contains(st, ".mp4") && !strings.Contains(st, "!!") {
		t.Fatalf("%s", st)
	}
	if files := git(t, proj, "ls-files"); strings.Contains(files, "mp4") || strings.Contains(files, "mov") || strings.Contains(files, "wav") {
		t.Fatalf("binary in git:\n%s", files)
	}
}

func TestSaveTitlePromptsModel(t *testing.T) {
	proj := t.TempDir()
	newProject(t, proj)
	put(t, proj, "notes.txt", "a")
	save(t, proj, "-m", "tighter intro", "--prompt", "make the intro punchier", "--prompt", "and cut the pause", "--model", "claude-opus-5-5", "--change", "intro")
	put(t, proj, "notes.txt", "b")
	save(t, proj, "-m", "plain", "--change", "notes")

	v1 := commitAt(t, proj, "v1")
	if v1["message"] != "tighter intro" || v1["model"] != "claude-opus-5-5" || fmt.Sprint(v1["prompts"]) != "[make the intro punchier and cut the pause]" {
		t.Fatalf("v1 commit.json: %v", v1)
	}
	if p := v1["prompts"].([]any); len(p) != 2 || p[0] != "make the intro punchier" {
		t.Fatalf("prompts out of order: %v", p)
	}
	v2c := commitAt(t, proj, "v2")
	if _, ok := v2c["model"]; ok {
		t.Fatalf("save without --model wrote one: %v", v2c)
	}

	out := mustRun(t, proj, nil, "log")
	blocks := strings.Split(out, "\n\n")
	for _, want := range []string{"tighter intro", `    prompts:  "make the intro punchier" ; "and cut the pause"`, "    model:    claude-opus-5-5"} {
		if !strings.Contains(blocks[1], want) {
			t.Fatalf("v1 log missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(blocks[0], "model:") {
		t.Fatalf("v2 log should have no model:\n%s", out)
	}
}

// An agent that cd'd into src/ must reach the project, not be told to make a new one there.
func TestCommandsWorkFromASubfolder(t *testing.T) {
	dir := t.TempDir()
	newProject(t, dir)
	src := filepath.Join(dir, "src")
	put(t, dir, "src/a.txt", "a")
	clip := filepath.Join(t.TempDir(), "clip.mp4")
	writeRandom(t, clip, 1000)
	rel, _ := filepath.Rel(src, clip)
	if out, code := v2(t, src, nil, "import", rel); code != 0 || !exists(filepath.Join(dir, "assets/imported/clip.mp4")) {
		t.Fatalf("import from src/: exit %d: %s", code, out)
	}
	save(t, src, "-m", "from src", "--change", "one")
	put(t, dir, "src/a.txt", "b")
	save(t, src, "-m", "again", "--change", "two")
	restore(t, src, "v1")
	if read(t, filepath.Join(dir, "src/a.txt")) != "a" {
		t.Fatal("restore from src/ didn't bring v1 back")
	}
	if exists(filepath.Join(src, ".git")) || exists(filepath.Join(src, ".v2")) {
		t.Fatal("a command made a project inside src/")
	}
}

// A fresh Linux box has no git identity and a hostname git won't guess an email from, so git
// refuses to commit. useConfigOnly makes git refuse the same way on any machine.
func TestSaveCommitsAsV2WithoutAGitIdentity(t *testing.T) {
	proj := t.TempDir()
	newProject(t, proj)
	put(t, proj, "src/a.txt", "a")
	noGuess := []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=user.useConfigOnly", "GIT_CONFIG_VALUE_0=true"}
	mustRun(t, proj, noGuess, "save", "--prompt", "p", "-m", "one", "--change", "one")
	if who := git(t, proj, "log", "-1", "--format=%an <%ae>"); who != "v2 <v2@localhost>\n" {
		t.Fatalf("author %q", who)
	}
}

// --render and --assemble belong to a version. With no file changed there is none to carry them.
func TestSaveRefusesARenderItCannotRecord(t *testing.T) {
	proj := t.TempDir()
	newProject(t, proj)
	writeRandom(t, filepath.Join(proj, "out/cut.mp4"), 1<<10)
	save(t, proj, "-m", "first", "--change", "first cut")
	if commitAt(t, proj, "v1")["render"] != nil {
		t.Fatal("v1 should have no video: the config names out/final.mp4")
	}

	again := []string{"save", "--prompt", "make it", "-m", "first", "--change", "first cut"}
	refused(t, proj, append(again, "--render", "out/cut.mp4"), "--render", ".v2/config.json")
	refused(t, proj, append(again, "--assemble", "sh scripts/render.sh"), "--assemble")
	refused(t, proj, append(again, "--render", "out/cut.mp4", "--assemble", "sh scripts/render.sh"), "--render", "--assemble")
	if out, code := v2(t, proj, gitIdentity, again...); code != 0 || !strings.Contains(out, "nothing changed since the last save") {
		t.Fatalf("a plain save with nothing changed: exit %d: %s", code, out)
	}

	// What the refusal says to do works, and saving the same flags again is not an error.
	put(t, proj, ".v2/config.json", `{"assemble": "sh scripts/render.sh", "render": "out/cut.mp4"}`)
	save(t, proj, "-m", "render", "--change", "the render is out/cut.mp4")
	if commitAt(t, proj, "v2")["render"] != "out/cut.mp4" {
		t.Fatalf("v2 render = %v", commitAt(t, proj, "v2")["render"])
	}
	mustRun(t, proj, gitIdentity, append(again, "--render", "out/cut.mp4", "--assemble", "sh scripts/render.sh")...) // flags the last version already has
}

// An agent that passes --render and --assemble on every save keeps doing so after a restore.
func TestRestoreKeepsTheRenderAndAssembleOfTheVersion(t *testing.T) {
	proj := t.TempDir()
	newProject(t, proj)
	flags := []string{"--render", "out/cut.mp4", "--assemble", "sh scripts/render.sh"}
	writeRandom(t, filepath.Join(proj, "out/cut.mp4"), 1<<10)
	save(t, proj, append([]string{"-m", "first", "--change", "first cut"}, flags...)...)
	writeRandom(t, filepath.Join(proj, "out/cut.mp4"), 1<<10)
	save(t, proj, append([]string{"-m", "second", "--change", "second cut"}, flags...)...)
	restore(t, proj, "v1")
	if c := commitAt(t, proj, "v3"); c["render"] != "out/cut.mp4" || c["assemble"] != "sh scripts/render.sh" {
		t.Fatalf("v3 restores v1 but records render %v, assemble %v", c["render"], c["assemble"])
	}
	save(t, proj, append([]string{"-m", "first", "--change", "first cut"}, flags...)...) // the same flags right after a restore
}

// After a restore the last version can record a render other than the config's. A flag that only
// repeats the config can't be fixed by setting the config, so the refusal says to drop it.
func TestSaveSaysToDropAFlagTheConfigAlreadyHolds(t *testing.T) {
	proj := t.TempDir()
	newProject(t, proj)
	writeRandom(t, filepath.Join(proj, "out/final.mp4"), 1<<10)
	writeRandom(t, filepath.Join(proj, "out/alt.mp4"), 1<<10)
	save(t, proj, "-m", "first", "--change", "first cut", "--render", "out/alt.mp4")
	put(t, proj, "src/edit.txt", "b")
	save(t, proj, "-m", "second", "--change", "second cut")
	restore(t, proj, "v1")

	again := []string{"save", "--prompt", "make it", "-m", "first", "--change", "first cut"}
	out, code := v2(t, proj, gitIdentity, append(again, "--render", "out/final.mp4")...)
	if code != 1 || !strings.Contains(out, "without --render") || strings.Contains(out, "set \"render\"") {
		t.Fatalf("--render that the config already holds: exit %d: %s", code, out)
	}
	mustRun(t, proj, gitIdentity, again...) // the same save without the flag
}

// On a FAT drive a render redone right after a save or a restore keeps its size, time and inode.
// save must still store the new bytes.
func TestSaveSeesARenderRedoneWithinTheDiskClockStep(t *testing.T) {
	if testing.Short() {
		t.Skip("mounts a disk image")
	}
	dir := filepath.Join(mountDisk(t, twoSecond), "proj")
	must(t, os.Mkdir(dir, 0o755))
	newProject(t, dir)
	put(t, dir, ".v2ignore", read(t, filepath.Join(dir, ".v2ignore"))+"._*\n") // the Mac's metadata files
	render := filepath.Join(dir, "out/final.mp4")
	writeRandom(t, render, 1<<20)
	save(t, dir, "-m", "one", "--change", "one")

	for _, c := range []struct{ after, then string }{{"save", "save"}, {"restore", "save"}, {"save", "restore"}} {
		for try := 1; ; try++ {
			writeRandom(t, render, 1<<20)
			save(t, dir, "-m", "render", "--change", "render")
			if c.after == "restore" {
				restore(t, dir, "v1")
			}
			placed, _ := os.Stat(render)
			want := sum(writeRandom(t, render, 1<<20))
			redone, _ := os.Stat(render)
			if !redone.ModTime().Equal(placed.ModTime()) {
				if try == 10 {
					t.Fatal("the disk never gave both renders the same time")
				}
				continue
			}
			if c.then == "restore" {
				refused(t, dir, []string{"restore", "v1"}, "out/final.mp4")
				if sum([]byte(read(t, render))) != want {
					t.Fatal("restore overwrote a render redone right after a save")
				}
				break
			}
			out := save(t, dir, "-m", "redo", "--change", "redo")
			if got := assets(t, dir)["out/final.mp4"].Sha256; got != want {
				t.Fatalf("a render redone right after a %s was not saved:\n%s", c.after, out)
			}
			break
		}
	}
}

// A full disk: the store moved to another disk and .v2/objects linked to it. save and restore still work.
func TestSaveAndRestoreWithTheStoreOnAnotherDisk(t *testing.T) {
	if testing.Short() {
		t.Skip("mounts a disk image")
	}
	dir, objects := t.TempDir(), filepath.Join(mountDisk(t, otherDisk), "objects")
	newProject(t, dir)
	must(t, errors.Join(os.Mkdir(objects, 0o755), os.Remove(filepath.Join(dir, ".v2/objects"))))
	must(t, os.Symlink(objects, filepath.Join(dir, ".v2/objects")))
	render := filepath.Join(dir, "out/final.mp4")
	writeRandom(t, render, 1<<20)
	save(t, dir, "-m", "one", "--change", "one")
	v1 := snapshot(t, dir)
	writeRandom(t, render, 1<<20)
	save(t, dir, "-m", "two", "--change", "two")
	restore(t, dir, "v1")
	sameFiles(t, "after restore v1", snapshot(t, dir), v1)
}

// A camera with its clock set ahead dates its files in the future. save must not read such a file again
// while it is unchanged (a long video would be read on every save), and must store it once it is rewritten.
func TestSaveDoesNotRereadAnUnchangedFileDatedInTheFuture(t *testing.T) {
	dir := t.TempDir()
	newProject(t, dir)
	clip := filepath.Join(dir, "assets/imported/cam.mp4")
	writeRandom(t, clip, 1<<20)
	ahead := time.Now().Add(24 * time.Hour)
	must(t, os.Chtimes(clip, time.Unix(0, 0), ahead))
	if read(t, clip); lastRead(t, clip) == 0 {
		t.Skip("this disk doesn't record when a file is read")
	}
	must(t, os.Chtimes(clip, time.Unix(0, 0), ahead))
	save(t, dir, "-m", "one", "--change", "one")
	before := lastRead(t, clip)

	if out := save(t, dir, "-m", "two", "--change", "two"); !strings.Contains(out, "nothing changed") {
		t.Fatalf("save saw a change in an unchanged file:\n%s", out)
	}
	if lastRead(t, clip) != before {
		t.Fatal("save read an unchanged file dated in the future again")
	}

	want := sum(writeRandom(t, clip, 1<<20))
	save(t, dir, "-m", "three", "--change", "three")
	if got := assets(t, dir)["assets/imported/cam.mp4"].Sha256; got != want {
		t.Fatal("a rewritten file that was dated in the future was not saved")
	}
}

// lastRead is the file's access time, which the disk moves on a read while it is older than the mtime.
func lastRead(t *testing.T, path string) int64 {
	t.Helper()
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		t.Fatal(err)
	}
	return st.Atim.Nano()
}

// On a drive that can't hold extended attributes (exFAT, FAT) the Mac keeps them in a ._ file next to
// each real file. Those aren't the user's work: no version holds them, in the store or in git.
func TestSaveSkipsAppleDouble(t *testing.T) {
	dir := t.TempDir()
	newProject(t, dir)
	writeRandom(t, filepath.Join(dir, "out/final.mp4"), 1<<10)
	writeRandom(t, filepath.Join(dir, "out/._final.mp4"), 1<<10)
	put(t, dir, "src/Video.tsx", "0:04\n")
	put(t, dir, "src/._Video.tsx", "Mac metadata\n")
	save(t, dir, "-m", "one", "--change", "one")
	if a := assets(t, dir); len(a) != 1 || a["out/final.mp4"].Sha256 == "" {
		t.Fatalf("assets: %v", a)
	}
	if tracked := git(t, dir, "ls-files"); strings.Contains(tracked, "._") || !strings.Contains(tracked, "src/Video.tsx") {
		t.Fatalf("git tracks:\n%s", tracked)
	}
	// A ._ file that comes and goes is not a change: there is nothing to save, and nothing blocks a restore.
	writeRandom(t, filepath.Join(dir, "out/._final.mp4"), 2<<10)
	put(t, dir, "src/._captions.srt", "Mac metadata\n")
	if out := save(t, dir, "-m", "two", "--change", "two"); !strings.Contains(out, "nothing changed") {
		t.Fatalf("save: %s", out)
	}
	restore(t, dir, "v1")
}

// A project shared through git or a zip can hold links where v2 writes its own files. v2 replaces
// them and never writes through them to the files they point at.
func TestPlantedLinksStayInside(t *testing.T) {
	proj, outside := t.TempDir(), t.TempDir()
	newProject(t, proj)
	save(t, proj, "-m", "first", "--change", "start")
	links := []string{".v2/clock", ".v2/hashcache", ".v2/hashcache.tmp", ".v2/assets.json.tmp", ".gitignore", "AGENTS.md", "CLAUDE.md"}
	plant := func(links ...string) {
		for _, l := range links {
			target := filepath.Join(outside, filepath.Base(l))
			must(t, os.WriteFile(target, []byte("keep\n"), 0o644))
			must(t, os.RemoveAll(filepath.Join(proj, l)))
			must(t, os.Symlink(target, filepath.Join(proj, l)))
		}
	}
	untouched := func(step string) {
		for _, l := range links {
			if got := read(t, filepath.Join(outside, filepath.Base(l))); got != "keep\n" {
				t.Fatalf("%s wrote through %s: %q", step, l, got)
			}
		}
	}

	plant(links...)
	mustRun(t, proj, nil, "init")
	untouched("init")
	plant(links...)
	put(t, proj, "notes.txt", "x")
	save(t, proj, "-m", "second", "--change", "notes")
	untouched("save")
	// A link in place of a tracked file is unsaved work that restore refuses to lose, so plant only in .v2.
	plant(links[:4]...)
	restore(t, proj, "v1")
	untouched("restore")
}
