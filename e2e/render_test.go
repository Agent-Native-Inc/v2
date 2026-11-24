package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderComesBackWithItsVersion(t *testing.T) {
	dir := t.TempDir()
	newProject(t, dir)
	put(t, dir, ".v2/config.json", `{"assemble": "render", "render": "out/final.mp4"}`)
	put(t, dir, "src/Video.tsx", "a\n")

	out, code := v2(t, dir, gitIdentity, "save", "--prompt", "make it", "-m", "no render yet", "--change", "x")
	if code != 0 || strings.Contains(out, "warning") {
		t.Fatalf("a save before the first render is normal, no warning: %s", out)
	}
	first := writeRandom(t, filepath.Join(dir, "out/final.mp4"), 1<<20)
	save(t, dir, "-m", "render", "--change", "y")
	put(t, dir, "src/Video.tsx", "b\n")
	writeRandom(t, filepath.Join(dir, "out/final.mp4"), 1<<20)
	save(t, dir, "-m", "re-render", "--change", "z")

	must(t, os.Remove(filepath.Join(dir, "out/final.mp4")))
	put(t, dir, "src/Video.tsx", "c\n")
	out, code = v2(t, dir, gitIdentity, "save", "--prompt", "make it", "-m", "render deleted", "--change", "w")
	if code != 0 || !strings.Contains(out, "v4 has no video") {
		t.Fatalf("a render that goes missing should warn: %s", out)
	}

	restore(t, dir, "v2")
	if got := read(t, filepath.Join(dir, "out/final.mp4")); got != string(first) {
		t.Fatal("restoring v2 didn't bring back v2's render")
	}
	if c := commitAt(t, dir, "v2"); c["render"] != "out/final.mp4" {
		t.Fatalf("v2 commit.json = %v", c)
	}
	if c := commitAt(t, dir, "v1"); c["render"] != nil {
		t.Fatalf("v1 had no render: %v", c)
	}
	log, _ := v2(t, dir, nil, "log")
	if !strings.Contains(log, "render:   out/final.mp4") {
		t.Fatalf("log: %s", log)
	}
}

// Remotion names its render out/Main.mp4 unless told otherwise; the default config says out/final.mp4.
func TestSaveNamesARenderUnderAnotherName(t *testing.T) {
	dir := t.TempDir()
	newProject(t, dir)
	writeRandom(t, filepath.Join(dir, "out/Main.mp4"), 1000)
	put(t, dir, "src/a.txt", "a")
	out := save(t, dir, "-m", "x", "--change", "y")
	if !strings.Contains(out, "out/Main.mp4") || !strings.Contains(out, `"render"`) {
		t.Fatalf("save didn't point at the video it found: %s", out)
	}
}
