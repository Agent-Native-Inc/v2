package e2e

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestLog(t *testing.T) {
	proj := t.TempDir()
	newProject(t, proj)
	if out, _ := v2(t, proj, nil, "log"); !strings.Contains(out, "no versions yet") {
		t.Fatalf("empty log: %s", out)
	}
	put(t, proj, ".v2/config.json", `{"assemble": "npx remotion render"}`)
	writeRandom(t, filepath.Join(proj, "clip.mov"), 1<<20)
	put(t, proj, "src/Video.tsx", "0:04\n")
	save(t, proj, "-m", "first cut", "--change", "intro 0:00-0:04")
	writeRandom(t, filepath.Join(proj, "out/final.mp4"), 1<<20)
	save(t, proj, "-m", "render", "--change", "rendered")
	// v3: edit code, add captions, replace the clip, delete the render.
	put(t, proj, "src/Video.tsx", "0:06\n")
	put(t, proj, "src/captions.srt", "hi\n")
	writeRandom(t, filepath.Join(proj, "clip.mov"), 1<<20)
	must(t, os.Remove(filepath.Join(proj, "out/final.mp4")))
	save(t, proj, "-m", "longer intro", "--change", "intro now 0:06")
	for i := 4; i <= 10; i++ {
		put(t, proj, "src/n.txt", strconv.Itoa(i))
		save(t, proj, "-m", "edit", "--change", "n")
	}

	out := mustRun(t, proj, nil, "log")
	blocks := strings.Split(out, "\n\n")
	if len(blocks) != 10 || !strings.HasPrefix(blocks[0], "v10 ") || !strings.HasPrefix(blocks[9], "v1 ") {
		t.Fatalf("want v10 first and v1 last:\n%s", out)
	}
	v3 := blocks[7]
	for _, want := range []string{
		"longer intro",
		"changes:  intro now 0:06",
		"recipe:   M src/Video.tsx, A src/captions.srt",
		"assets:   M clip.mov, - out/final.mp4",
		"assemble: npx remotion render",
	} {
		if !strings.Contains(v3, want) {
			t.Fatalf("v3 block missing %q:\n%s", want, v3)
		}
	}
	if !strings.Contains(blocks[9], "assets:   + clip.mov") || !strings.Contains(blocks[9], "A src/Video.tsx") {
		t.Fatalf("v1 should show everything as added:\n%s", blocks[9])
	}
}

func TestLogShowsCutsFromOldCommitJSON(t *testing.T) {
	proj := t.TempDir()
	newProject(t, proj)
	put(t, proj, "notes.txt", "x")
	save(t, proj, "-m", "trim", "--change", "music quieter")
	old := `{"version":"v1","message":"trim","changes":[{"text":"music quieter"}],"cuts":[{"at":3.4,"removed":3.4,"text":"removed the slow bit"}],"assemble":"","saved_at":"2026-10-01T00:00:00Z"}`
	put(t, proj, ".v2/commit.json", old)
	git(t, proj, "commit", "-q", "--amend", "--no-edit", ".v2/commit.json")
	git(t, proj, "tag", "-f", "v1")
	out, code := v2(t, proj, nil, "log")
	if code != 0 || !strings.Contains(out, "changes:  cut 0:03.4 3.4s removed the slow bit; music quieter") {
		t.Fatalf("exit %d: %s", code, out)
	}
}

func TestLogOutsideAProject(t *testing.T) {
	if out, code := v2(t, t.TempDir(), nil, "log"); code != 1 || !strings.Contains(out, "v2 init") {
		t.Fatalf("exit %d: %s", code, out)
	}
}
