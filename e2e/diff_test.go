package e2e

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveStoresStructuredChanges(t *testing.T) {
	proj := t.TempDir()
	newProject(t, proj)
	must(t, os.WriteFile(filepath.Join(proj, "notes.txt"), []byte("x"), 0o644))
	save(t, proj, "-m", "zoom", "--change", "0:04.5-0:06.5 zoom on the name", "--change", "cut 0:03.4 3.4s removed the slow bit", "--change", "music quieter")
	c := commitAt(t, proj, "v1")
	if got := mustJSON(t, c["changes"]); got != `[{"from":4.5,"text":"zoom on the name","to":6.5},{"from":3.4,"kind":"cut","removed":3.4,"text":"removed the slow bit","to":3.4},{"text":"music quieter"}]` {
		t.Fatalf("changes = %s", got)
	}
	if cuts, ok := c["cuts"]; ok {
		t.Fatalf("commit.json still has a separate cuts list: %v", cuts)
	}
	// A malformed line is refused before anything is saved.
	must(t, os.WriteFile(filepath.Join(proj, "notes.txt"), []byte("y"), 0o644))
	if out, code := v2(t, proj, gitIdentity, "save", "--prompt", "make it", "-m", "x", "--change", "0:09-0:04 backwards"); code != 1 || !strings.Contains(out, "ends before it starts") {
		t.Fatalf("exit %d: %s", code, out)
	}
	if tags := git(t, proj, "tag", "--list"); tags != "v1\n" {
		t.Fatalf("a refused save made a version: %q", tags)
	}
}

func TestDiffServesVersionsAndRenders(t *testing.T) {
	proj := t.TempDir()
	newProject(t, proj)
	must(t, os.WriteFile(filepath.Join(proj, ".v2/config.json"), []byte(`{"render": "out/final.mp4"}`), 0o644))
	must(t, os.MkdirAll(filepath.Join(proj, "out"), 0o755))
	writeRandom(t, filepath.Join(proj, "out/final.mp4"), 1<<20)
	save(t, proj, "-m", "first", "--change", "0:01-0:02 title card")
	render := read(t, filepath.Join(proj, "out/final.mp4"))
	must(t, os.Remove(filepath.Join(proj, "out/final.mp4"))) // the page must play the stored copy, not the folder's
	must(t, os.WriteFile(filepath.Join(proj, "notes.txt"), []byte("x"), 0o644))
	save(t, proj, "-m", "second", "--change", "no render this time")

	url := serveDiff(t, proj)

	var api struct {
		Versions []struct {
			Version string           `json:"version"`
			Video   bool             `json:"video"`
			Changes []map[string]any `json:"changes"`
		} `json:"versions"`
	}
	if err := json.Unmarshal([]byte(get(t, url+"api/versions", "", 200)), &api); err != nil {
		t.Fatal(err)
	}
	if len(api.Versions) != 2 || !api.Versions[0].Video || api.Versions[1].Video || api.Versions[0].Changes[0]["text"] != "title card" {
		t.Fatalf("api = %+v", api)
	}
	// Seeking needs range requests; the bytes are the render as saved.
	if got := get(t, url+"video/v1", "bytes=100-199", 206); got != render[100:200] {
		t.Fatal("range request returned the wrong bytes")
	}
	if get(t, url+"video/v1", "", 200) != render {
		t.Fatal("video differs from the saved render")
	}
	get(t, url+"video/v2", "", 404) // v2 has no render
	get(t, url+"video/v9", "", 404)
	get(t, url+"poster/--output=x", "", 404)
	get(t, url+"video/..%2f..%2fetc", "", 404)
}

// serveDiff runs `v2 diff` in proj until the test ends and returns the page's URL.
func serveDiff(t *testing.T, proj string) string {
	t.Helper()
	cmd := exec.Command(bin, "diff", "--no-open")
	cmd.Dir = proj
	stdout, err := cmd.StdoutPipe()
	must(t, err)
	must(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	line, _ := bufio.NewReader(stdout).ReadString('\n') // "versions of <dir> at http://127.0.0.1:<port>/ ..."
	_, url, ok := strings.Cut(line, "http://")
	if !ok {
		t.Fatalf("no URL in %q", line)
	}
	return "http://" + strings.Fields(url)[0]
}

func get(t *testing.T, url, rng string, want int) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	must(t, err)
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(res.Body)
	must(t, errors.Join(err, res.Body.Close()))
	if res.StatusCode != want {
		t.Fatalf("GET %s: %d, want %d", url, res.StatusCode, want)
	}
	return string(b)
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Each change can name its kind and the file that makes it; a file the project doesn't have is refused.
func TestSaveStoresChangeSources(t *testing.T) {
	proj := t.TempDir()
	newProject(t, proj)
	writeRandom(t, filepath.Join(proj, "assets/generated/vo-intro.mp3"), 1<<12)
	put(t, proj, "src/Titles.tsx", "x")
	save(t, proj, "-m", "intro",
		"--change", "0:02-0:09.8 voice assets/generated/vo-intro.mp3: voice-over for the intro",
		"--change", "0:06 text src/Titles.tsx: 'Introducing V2' slides in",
		"--change", "music 20% quieter")
	got := mustJSON(t, commitAt(t, proj, "v1")["changes"])
	want := `[{"from":2,"kind":"voice","src":"assets/generated/vo-intro.mp3","text":"voice-over for the intro","to":9.8},` +
		`{"from":6,"kind":"text","src":"src/Titles.tsx","text":"'Introducing V2' slides in","to":6},{"text":"music 20% quieter"}]`
	if strings.TrimSpace(got) != want {
		t.Fatalf("changes =\n%s\nwant\n%s", got, want)
	}

	put(t, proj, "src/Titles.tsx", "y")
	save(t, proj, "-m", "ending",
		"--change", "0:12-0:15 added video assets/generated/vo-intro.mp3: final screen",
		"--change", "0:04-0:09 retimed runs faster",
		"--change", "added grain everywhere")
	got = mustJSON(t, commitAt(t, proj, "v2")["changes"])
	want = `[{"from":12,"kind":"video","src":"assets/generated/vo-intro.mp3","text":"final screen","to":15,"type":"added"},` +
		`{"from":4,"text":"runs faster","to":9,"type":"retimed"},{"text":"added grain everywhere"}]`
	if strings.TrimSpace(got) != want {
		t.Fatalf("changes =\n%s\nwant\n%s", got, want)
	}
	if msg := git(t, proj, "log", "-1", "--format=%b"); !strings.Contains(msg, "0:12-0:15 added final screen; 0:04-0:09 retimed runs faster; added grain everywhere") {
		t.Fatalf("commit message: %q", msg)
	}

	put(t, proj, "src/Titles.tsx", "z")
	out, code := v2(t, proj, gitIdentity, "save", "--prompt", "make it", "-m", "x", "--change", "0:01-0:02 voice assets/generated/missing.mp3: new voice")
	if code != 1 || !strings.Contains(out, "assets/generated/missing.mp3, which isn't in the project") {
		t.Fatalf("exit %d: %s", code, out)
	}
	if tags := git(t, proj, "tag", "--list"); tags != "v1\nv2\n" {
		t.Fatalf("a refused save made a version: %q", tags)
	}
}
