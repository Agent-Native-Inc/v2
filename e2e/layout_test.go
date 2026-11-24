package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewProjectHasTheLayout(t *testing.T) {
	dir := t.TempDir()
	newProject(t, dir)
	for _, d := range []string{"assets/imported", "assets/generated", "assets/scratch", "src", "scripts", "out"} {
		if fi, err := os.Stat(filepath.Join(dir, d)); err != nil || !fi.IsDir() {
			t.Fatalf("%s/ missing", d)
		}
	}
	if !strings.Contains(read(t, filepath.Join(dir, "README.md")), "How it was made") {
		t.Fatal("README.md should ask how the video was made")
	}
	newProject(t, dir) // running init again is safe
	put(t, dir, "src/Video.tsx", "a\n")
	writeRandom(t, filepath.Join(dir, "out/final.mp4"), 1<<20)
	save(t, dir, "-m", "x", "--change", "y")
	if _, ok := commitAt(t, dir, "v1")["ffmpeg"]; ok {
		t.Fatal("commit.json still records ffmpeg")
	}
	// .gitignore is in git, so whoever gets the project gets the rules too.
	if files := git(t, dir, "ls-files"); !strings.Contains(files, ".gitignore") || strings.Contains(files, "final.mp4") {
		t.Fatalf("want .gitignore in git and the render out of it:\n%s", files)
	}
}

// init in a folder that already has files adds the standard folders and moves nothing;
// files outside the layout are still saved, with a note.
func TestInitInAnExistingFolder(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, "notes.txt", "mine\n")
	newProject(t, dir)
	if !exists(filepath.Join(dir, "assets/imported")) {
		t.Fatal("init should add the standard folders")
	}
	if read(t, filepath.Join(dir, "notes.txt")) != "mine\n" {
		t.Fatal("init changed an existing file")
	}
	writeRandom(t, filepath.Join(dir, "clip.mov"), 1<<20)
	put(t, dir, "render.py", "print(1)\n")
	if out, code := v2(t, dir, gitIdentity, "save", "--prompt", "make it", "-m", "x", "--change", "y"); code != 0 || !strings.Contains(out, "clip.mov") {
		t.Fatalf("want a saved version with a note on clip.mov, exit %d:\n%s", code, out)
	}
}

// Files outside the layout are saved where they are, with a note on where they belong. Only files a
// save would lose (text in assets/ or out/, which git skips and v2 doesn't store) block it.
func TestMisplacedFilesAreSavedWithAWarning(t *testing.T) {
	for _, c := range []struct {
		path, hint      string
		binary, refused bool
	}{
		{"render.py", "scripts/", false, false},
		{"Video.tsx", "src/", false, false},
		{"clip.mov", "assets/imported/", true, false},
		{"src/bg.png", "assets/generated/", true, false},
		{"scripts/out.wav", "assets/generated/", true, false},
		{"assets/x.png", "assets/imported/ (files you brought)", true, false},
		{"public/src.mp4", "Config.setPublicDir('assets')", true, false},
		{"misc/notes.txt", "only assets/, src/, scripts/ and out/", false, false},
		{"assets/generated/timeline.json", "goes in src/", false, true},
		{"assets/lottie/anim.json", "goes in src/", false, true},
		{"out/notes.txt", "out/ holds the render only", false, true},
	} {
		path, hint := c.path, c.hint
		t.Run(path, func(t *testing.T) {
			dir := t.TempDir()
			newProject(t, dir)
			put(t, dir, "src/Video.tsx", "a\n")
			if c.binary {
				writeRandom(t, filepath.Join(dir, path), 1<<20)
			} else {
				put(t, dir, path, "x\n")
			}
			if c.refused {
				refused(t, dir, []string{"save", "--prompt", "make it", "-m", "x", "--change", "y"}, path, hint, "nothing saved")
				return
			}
			out, code := v2(t, dir, gitIdentity, "save", "--prompt", "make it", "-m", "x", "--change", "y")
			if code != 0 || !strings.Contains(out, path) || !strings.Contains(out, hint) || !strings.Contains(out, "warning: saved") {
				t.Fatalf("want a saved version and a note on %s, got exit %d:\n%s", path, code, out)
			}
			if !strings.Contains(git(t, dir, "ls-files")+read(t, filepath.Join(dir, ".v2/assets.json")), path) {
				t.Fatalf("%s was not saved", path)
			}
		})
	}
}

func TestSaveAcceptsTheLayout(t *testing.T) {
	dir := t.TempDir()
	newProject(t, dir)
	for _, p := range []string{"src/Video.tsx", "scripts/proxy.sh", "remotion.config.ts", "tsconfig.json", ".claude/settings.json", "src/captions.srt", "src/timeline.json"} {
		put(t, dir, p, "x\n")
	}
	for _, p := range []string{"assets/imported/fonts/Inter.ttf", "assets/generated/gemini/cover.png", "out/final.mp4"} {
		writeRandom(t, filepath.Join(dir, p), 1<<20)
	}
	save(t, dir, "-m", "x", "--change", "y")
	if a := assets(t, dir); a["assets/imported/fonts/Inter.ttf"].Source != "imported" || a["assets/generated/gemini/cover.png"].Source != "derived" {
		t.Fatalf("sources come from the folder: %v", a)
	}
}

// A redo can overwrite a file in assets/ under the same name; every version still gets its own bytes back.
func TestAssetsCanBeReplaced(t *testing.T) {
	dir := t.TempDir()
	newProject(t, dir)
	first := writeRandom(t, filepath.Join(dir, "assets/generated/voice.mp3"), 1<<20)
	save(t, dir, "-m", "x", "--change", "y")
	second := writeRandom(t, filepath.Join(dir, "assets/generated/voice.mp3"), 1<<20)
	save(t, dir, "-m", "slower voice", "--change", "voiceover slower")
	restore(t, dir, "v1")
	if read(t, filepath.Join(dir, "assets/generated/voice.mp3")) != string(first) {
		t.Fatal("v1's voice.mp3 should come back")
	}
	restore(t, dir, "v2")
	if read(t, filepath.Join(dir, "assets/generated/voice.mp3")) != string(second) {
		t.Fatal("v2's voice.mp3 should come back")
	}
}

func TestV2ignoreKeepsCachesAndSecretsOut(t *testing.T) {
	dir := t.TempDir()
	newProject(t, dir)
	put(t, dir, "src/Video.tsx", "a\n")
	put(t, dir, ".env", "OPENAI_API_KEY=sk-1\n")
	put(t, dir, "venv/lib/x.py", "x\n")
	for _, p := range []string{"venv/bin/python", ".cache/huggingface/model.safetensors", "media/videos/partial.mp4", "scratch/take.mp4", "scripts/music/target/release/music"} {
		writeRandom(t, filepath.Join(dir, p), 1<<20)
	}
	if out, _ := v2(t, dir, gitIdentity, "save", "--prompt", "make it", "-m", "x", "--change", "y"); !strings.Contains(out, "scratch/take.mp4") { // not listed yet
		t.Fatalf("want a note on scratch/take.mp4:\n%s", out)
	}

	appendTo(t, filepath.Join(dir, ".v2ignore"), "scratch/\n")                  // what the agent does for a tool's scratch folder
	put(t, dir, "src/hyperframes/.hyperframes/artifacts/checks/a.json", "{}\n") // Hyperframes keeps its project under src/
	save(t, dir, "-m", "x", "--change", "y")
	assets := read(t, filepath.Join(dir, ".v2/assets.json"))
	files := git(t, dir, "ls-files")
	for _, p := range []string{".env", "venv/", ".cache/", "media/", "scratch/", ".hyperframes", "target/"} {
		if strings.Contains(assets, `"`+p) || strings.Contains(assets, "/"+p) || strings.Contains(files, p) { // at the top or nested, like scripts/music/target/
			t.Fatalf("%s was saved:\n%s\n%s", p, assets, files)
		}
	}
	if ignore := read(t, filepath.Join(dir, ".gitignore")); strings.Contains(ignore, "venv") || strings.Contains(ignore, "scratch") {
		t.Fatalf(".gitignore should list only what v2 stores; .v2ignore holds the rest:\n%s", ignore)
	}
	if !strings.Contains(files, ".v2ignore") {
		t.Fatal(".v2ignore should travel with the project")
	}
}

// A restore keeps today's .v2ignore: what's ignored now stays ignored after going back.
func TestRestoreKeepsTodaysV2ignore(t *testing.T) {
	dir := t.TempDir()
	newProject(t, dir)
	put(t, dir, "src/Video.tsx", "a\n")
	save(t, dir, "-m", "v1", "--change", "x")

	appendTo(t, filepath.Join(dir, ".v2ignore"), "scratch/\n")
	writeRandom(t, filepath.Join(dir, "scratch/take.mp4"), 1<<20)
	put(t, dir, "src/Video.tsx", "b\n")
	save(t, dir, "-m", "v2", "--change", "x")

	restore(t, dir, "v1") // v1's .v2ignore has no scratch/, so this would refuse or store it
	if !strings.Contains(read(t, filepath.Join(dir, ".v2ignore")), "scratch/") {
		t.Fatal("restore rolled .v2ignore back")
	}
	if strings.Contains(read(t, filepath.Join(dir, ".v2/assets.json")), "scratch/") {
		t.Fatal("scratch/ was stored by the restore")
	}
}

// People and agents rewrite .gitignore; the store must stay out of git anyway.
func TestStoreStaysOutOfGitWhenGitignoreIsReplaced(t *testing.T) {
	dir := t.TempDir()
	newProject(t, dir)
	put(t, dir, "src/Video.tsx", "a\n")
	put(t, dir, ".gitignore", "node_modules\n")
	writeRandom(t, filepath.Join(dir, "out/final.mp4"), 1<<20)
	save(t, dir, "-m", "x", "--change", "y")
	if files := git(t, dir, "ls-files"); strings.Contains(files, ".v2/objects") || strings.Contains(files, ".v2/hashcache") {
		t.Fatalf("the store went into git:\n%s", files)
	}
}

// Media lives only in assets/ and out/, so git skips both whole folders instead of one line per file.
func TestAssetsAndOutStayOutOfGit(t *testing.T) {
	dir := t.TempDir()
	newProject(t, dir)
	put(t, dir, "src/Video.tsx", "a\n")
	for _, p := range []string{"assets/generated/music.wav", "assets/imported/clip.mov", "out/final.mp4"} {
		writeRandom(t, filepath.Join(dir, p), 1<<20)
	}
	save(t, dir, "-m", "x", "--change", "y")
	ignore := read(t, filepath.Join(dir, ".gitignore"))
	if !strings.Contains(ignore, "\n/assets/\n") || !strings.Contains(ignore, "\n/out/\n") || strings.Contains(ignore, "music.wav") {
		t.Fatalf("want whole folders, not one line per file:\n%s", ignore)
	}
	if files := git(t, dir, "ls-files"); strings.Contains(files, "assets/") || strings.Contains(files, "out/") {
		t.Fatalf("media went into git:\n%s", files)
	}
}

func TestImportRefusesText(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	newProject(t, dir)
	put(t, outside, "captions.srt", "1\n00:00:01,000 --> 00:00:02,000\nhi\n")
	refused(t, dir, []string{"import", filepath.Join(outside, "captions.srt")}, "captions.srt", "src/")
	if exists(filepath.Join(dir, "assets/imported/captions.srt")) {
		t.Fatal("a refused import still copied the file")
	}
}

// A misplaced folder is one line, not one per file; frames in src/ point at assets/scratch/.
func TestWarningGroupsByFolder(t *testing.T) {
	dir := t.TempDir()
	newProject(t, dir)
	put(t, dir, "music/Cargo.toml", "[package]\n")
	for i := range 40 {
		put(t, dir, fmt.Sprintf("music/gen/step%d.txt", i), "x\n")
		writeRandom(t, filepath.Join(dir, fmt.Sprintf("src/mc/output/%03d.png", i)), 1<<10)
	}
	out, code := v2(t, dir, gitIdentity, "save", "--prompt", "make it", "-m", "x", "--change", "y")
	for _, w := range []string{"music/ (41 files)", "src/mc/output/ (40 files)", "assets/scratch/<tool>/"} {
		if code != 0 || !strings.Contains(out, w) {
			t.Fatalf("want %q in a saved version's warning, exit %d:\n%s", w, code, out)
		}
	}
	if n := strings.Count(out, "\n"); n > 6 {
		t.Fatalf("want a few lines, got %d:\n%s", n, out)
	}
}

// An ignored folder the user can't read, like a docker Postgres data dir owned by another user,
// is skipped unopened, so save doesn't fail on it.
func TestSaveSkipsUnreadableIgnoredFolders(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every folder")
	}
	dir := t.TempDir()
	newProject(t, dir)
	put(t, dir, "src/Video.tsx", "a\n")
	put(t, dir, ".cache/pg/data/base", "x\n")
	put(t, dir, "node_modules/x/index.js", "y\n")
	for _, p := range []string{".cache/pg/data", "node_modules/x"} {
		must(t, os.Chmod(filepath.Join(dir, p), 0))
		t.Cleanup(func() { must(t, os.Chmod(filepath.Join(dir, p), 0o755)) })
	}
	save(t, dir, "-m", "x", "--change", "y")
}

// Restoring to before a Remotion subproject leaves remotion/ holding only its node_modules,
// with thousands of scoped packages. Saves there must keep working on git 2.25 (Ubuntu 20.04).
func TestSaveWithNodeModulesLeftInAnUntrackedFolder(t *testing.T) {
	dir := t.TempDir()
	newProject(t, dir)
	put(t, dir, "src/Video.tsx", "a\n")
	for i := range 3000 {
		put(t, dir, fmt.Sprintf("remotion/node_modules/@remotion/package-number-%d/dist/index.js", i), "x\n")
	}
	save(t, dir, "-m", "x", "--change", "y")
	if assets := read(t, filepath.Join(dir, ".v2/assets.json")); strings.Contains(assets, "node_modules") {
		t.Fatalf("node_modules was saved:\n%s", assets)
	}
}

// A tool's working media lives in assets/scratch/: on disk with the other media, never saved.
func TestScratchIsNeverSaved(t *testing.T) {
	dir := t.TempDir()
	newProject(t, dir)
	put(t, dir, "src/Video.tsx", "a\n")
	writeRandom(t, filepath.Join(dir, "assets/scratch/manim/videos/partial.mp4"), 1<<20)
	save(t, dir, "-m", "x", "--change", "y")
	if a := read(t, filepath.Join(dir, ".v2/assets.json")); strings.Contains(a, "scratch") {
		t.Fatalf("scratch was saved:\n%s", a)
	}
	writeRandom(t, filepath.Join(dir, "assets/scratch/manim/videos/partial.mp4"), 1<<20) // changing it is fine
	put(t, dir, "src/Video.tsx", "b\n")
	save(t, dir, "-m", "x", "--change", "z")
}

// Nothing a project needs is dropped or refused by mistake.
func TestNothingNeededIsLost(t *testing.T) {
	dir := t.TempDir()
	newProject(t, dir)
	put(t, dir, "src/media/clips.ts", "export const a = 1\n") // tool-output names, deeper than the top
	put(t, dir, "src/build/steps.ts", "export const b = 1\n")
	writeRandom(t, filepath.Join(dir, "assets/generated/build/intro.png"), 1<<10)
	put(t, dir, "assets/generated/logo.svg", "<svg xmlns='http://www.w3.org/2000/svg'/>\n")
	put(t, dir, ".prettierrc.cjs", "module.exports = {}\n")
	put(t, dir, "bun.lockb", "\x00bun lockfile") // binary by its NUL, which random bytes can lack
	save(t, dir, "-m", "x", "--change", "y")
	files, assets := git(t, dir, "ls-files"), read(t, filepath.Join(dir, ".v2/assets.json"))
	for _, p := range []string{"src/media/clips.ts", "src/build/steps.ts", ".prettierrc.cjs"} {
		if !strings.Contains(files, p) {
			t.Errorf("%s not in git:\n%s", p, files)
		}
	}
	for _, p := range []string{"assets/generated/build/intro.png", "assets/generated/logo.svg", "bun.lockb"} {
		if !strings.Contains(assets, p) {
			t.Errorf("%s not stored:\n%s", p, assets)
		}
	}
}

// A starter that ran git init in its folder would make git keep a pointer instead of the files.
func TestNestedGitRepoIsRefused(t *testing.T) {
	dir := t.TempDir()
	newProject(t, dir)
	put(t, dir, "src/remotion/Root.tsx", "a\n")
	git(t, filepath.Join(dir, "src/remotion"), "init", "-q")
	git(t, filepath.Join(dir, "src/remotion"), "add", "-A")
	git(t, filepath.Join(dir, "src/remotion"), "commit", "-qm", "starter")
	refused(t, dir, []string{"save", "--prompt", "make it", "-m", "x", "--change", "y"}, "src/remotion", ".git")
	must(t, os.RemoveAll(filepath.Join(dir, "src/remotion/.git")))
	save(t, dir, "-m", "x", "--change", "y")
	if files := git(t, dir, "ls-files"); !strings.Contains(files, "src/remotion/Root.tsx") {
		t.Fatalf("the starter's files should be in git now:\n%s", files)
	}
}

// git skips assets/, and v2 doesn't follow links: a linked clip would silently vanish from versions.
func TestLinkInAssetsIsRefused(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	newProject(t, dir)
	writeRandom(t, filepath.Join(outside, "clip.mov"), 1<<10)
	must(t, os.Symlink(filepath.Join(outside, "clip.mov"), filepath.Join(dir, "assets/imported/clip.mov")))
	refused(t, dir, []string{"save", "--prompt", "make it", "-m", "x", "--change", "y"}, "assets/imported/clip.mov", "v2 import")
}

// People edit .gitignore by hand; losing v2's end marker must not drop their own lines.
func TestGitignoreLinesSurviveAMissingMarker(t *testing.T) {
	dir := t.TempDir()
	newProject(t, dir)
	put(t, dir, "src/Video.tsx", "a\n")
	save(t, dir, "-m", "x", "--change", "y")
	g := strings.Replace(read(t, filepath.Join(dir, ".gitignore")), "# end v2\n", "", 1) + "*.log\n"
	put(t, dir, ".gitignore", g)
	put(t, dir, "src/Video.tsx", "b\n")
	save(t, dir, "-m", "x", "--change", "z")
	if g := read(t, filepath.Join(dir, ".gitignore")); !strings.Contains(g, "*.log") {
		t.Fatalf("the user's line was dropped:\n%s", g)
	}
}

// .v2ignore means exactly what it would mean in .gitignore, ** and ! included.
func TestV2ignoreIsReadLikeGitignore(t *testing.T) {
	dir := t.TempDir()
	newProject(t, dir)
	appendTo(t, filepath.Join(dir, ".v2ignore"), "*.log\n!keep.log\nsrc/**/cache/\n")
	put(t, dir, "a.log", "x\n")
	put(t, dir, "keep.log", "x\n")
	put(t, dir, "src/a/b/cache/x.ts", "x\n")
	put(t, dir, "src/Video.tsx", "x\n")
	writeRandom(t, filepath.Join(dir, "src/a/b/cache/frame.png"), 1<<10) // would be refused if v2 didn't skip it
	save(t, dir, "-m", "x", "--change", "y")
	files := git(t, dir, "ls-files")
	if strings.Contains(files, "a.log") || strings.Contains(files, "cache/") || !strings.Contains(files, "keep.log") {
		t.Fatalf("want keep.log only:\n%s", files)
	}
}

// A line break in a media file's name would split its .gitignore line in two; the second half
// ("Video.tsx" here) would then hide every file with that name, so the save refuses instead.
func TestLineBreakInMediaNameIsRefused(t *testing.T) {
	dir := t.TempDir()
	newProject(t, dir)
	put(t, dir, "src/Video.tsx", "a\n")
	writeRandom(t, filepath.Join(dir, "src/b\nVideo.tsx"), 1<<20)
	refused(t, dir, []string{"save", "--prompt", "make it", "-m", "x", "--change", "y"}, "nothing saved", "line break", `"src/b\nVideo.tsx"`)
}
