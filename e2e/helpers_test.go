package e2e

// Helpers the tests share: running v2 and git, making projects, and reading what v2 wrote.

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var gitIdentity = []string{"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t"}

// runBin runs binary in dir and returns combined output and exit code.
func runBin(t *testing.T, binary, dir string, env []string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, _ := cmd.CombinedOutput()
	return string(out), cmd.ProcessState.ExitCode()
}

// v2 runs the binary under test in dir and returns combined output and exit code.
func v2(t *testing.T, dir string, env []string, args ...string) (string, int) {
	t.Helper()
	return runBin(t, bin, dir, env, args...)
}

// mustRun runs v2 in dir, fails the test unless it exits 0, and returns its output.
func mustRun(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	out, code := v2(t, dir, env, args...)
	if code != 0 {
		t.Fatalf("%v: exit %d: %s", args, code, out)
	}
	return out
}

// refused runs v2 in dir, fails the test unless it exits 1 naming every want, and checks it made no version.
func refused(t *testing.T, dir string, args []string, want ...string) {
	t.Helper()
	tags := git(t, dir, "tag", "--list")
	out, code := v2(t, dir, gitIdentity, args...)
	if code != 1 {
		t.Fatalf("%v: want refusal, got exit %d: %s", args, code, out)
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Fatalf("%v: output should mention %q:\n%s", args, w, out)
		}
	}
	if after := git(t, dir, "tag", "--list"); after != tags {
		t.Fatalf("%v: a refusal changed the versions from %q to %q", args, tags, after)
	}
}

func newProject(t *testing.T, dir string) {
	t.Helper()
	mustRun(t, dir, nil, "init")
}

func save(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if !slices.Contains(args, "--prompt") {
		args = append(args, "--prompt", "make it")
	}
	return mustRun(t, dir, gitIdentity, append([]string{"save"}, args...)...)
}

func restore(t *testing.T, dir, v string) string {
	t.Helper()
	return mustRun(t, dir, gitIdentity, "restore", v)
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	args = append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s", args, out)
	}
	return string(out)
}

// clean fails the test if git sees any change in dir.
func clean(t *testing.T, dir string) {
	t.Helper()
	if st := git(t, dir, "status", "--porcelain"); st != "" {
		t.Fatalf("not clean:\n%s", st)
	}
}

// must fails the test on a setup step's error.
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	must(t, err)
	return string(b)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// put writes content to rel in dir, making its folder.
func put(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	must(t, os.MkdirAll(filepath.Dir(p), 0o755))
	must(t, os.WriteFile(p, []byte(content), 0o644))
}

// writeRandom writes n random bytes to path, making its folder, and returns them.
func writeRandom(t *testing.T, path string, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	rand.Read(b)
	must(t, os.MkdirAll(filepath.Dir(path), 0o755))
	must(t, os.WriteFile(path, b, 0o644))
	return b
}

// appendTo adds text to the end of the file at path, making it if it's missing.
func appendTo(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	must(t, err)
	_, err = f.WriteString(text)
	must(t, errors.Join(err, f.Close()))
}

// ffmpegRender makes a one-second 10 fps test video at out from a lavfi source like "testsrc=size=320x240".
func ffmpegRender(t *testing.T, out, source string, args ...string) {
	t.Helper()
	args = append([]string{"-v", "error", "-y", "-f", "lavfi", "-i", source + ":rate=10:duration=1"}, args...)
	if b, err := exec.Command("ffmpeg", append(args, "-pix_fmt", "yuv420p", out)...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %s", b)
	}
}

func sum(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }

// objectPath is where dir's store keeps the blob with this sha256.
func objectPath(dir, sha string) string {
	return filepath.Join(dir, ".v2/objects", sha[:2], sha[2:])
}

// storeObject puts b in dir's store and returns its sha256.
func storeObject(t *testing.T, dir string, b []byte) string {
	t.Helper()
	sha := sum(b)
	must(t, os.MkdirAll(filepath.Dir(objectPath(dir, sha)), 0o755))
	must(t, os.WriteFile(objectPath(dir, sha), b, 0o644))
	return sha
}

type asset struct {
	Sha256 string `json:"sha256"`
	Source string `json:"source"`
	Size   int64  `json:"size"`
}

// assets is what dir's .v2/assets.json lists now.
func assets(t *testing.T, dir string) map[string]asset {
	t.Helper()
	m := map[string]asset{}
	must(t, json.Unmarshal([]byte(read(t, filepath.Join(dir, ".v2/assets.json"))), &m))
	return m
}

// assetsAt is what the version tagged tag listed in its .v2/assets.json.
func assetsAt(t *testing.T, dir, tag string) map[string]asset {
	t.Helper()
	m := map[string]asset{}
	must(t, json.Unmarshal([]byte(git(t, dir, "show", tag+":.v2/assets.json")), &m))
	return m
}

func commitAt(t *testing.T, dir, tag string) map[string]any {
	t.Helper()
	m := map[string]any{}
	must(t, json.Unmarshal([]byte(git(t, dir, "show", tag+":.v2/commit.json")), &m))
	return m
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	must(t, err)
	return string(b)
}

// recode turns from into to through JSON, as the site would read what v2 sent.
func recode(t *testing.T, from, to any) {
	t.Helper()
	must(t, json.Unmarshal([]byte(mustJSON(t, from)), to))
}

// envValue is the value env gives key; the last one wins, as it does for a command run with env.
func envValue(env []string, key string) string {
	v := ""
	for _, e := range env {
		if rest, ok := strings.CutPrefix(e, key+"="); ok {
			v = rest
		}
	}
	return v
}

// commentForm is the multipart body the diff page posts for a comment, and its content type.
func commentForm(t *testing.T, fields map[string]string, drawing []byte) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for k, v := range fields {
		must(t, form.WriteField(k, v))
	}
	if drawing != nil {
		f, err := form.CreateFormFile("drawing", "frame.png")
		must(t, err)
		_, err = f.Write(drawing)
		must(t, err)
	}
	must(t, form.Close())
	return &body, form.FormDataContentType()
}
