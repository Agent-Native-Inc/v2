package e2e

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

var (
	builds     sync.Once
	releaseKey ed25519.PrivateKey
	oldBin     string // v0.9.0, trusting releaseKey instead of v2.video's key
	newBin     string // v0.9.1
	otherBin   string // what the other platforms' files hold, so fetching the wrong one fails
)

func buildReleases(t *testing.T) {
	t.Helper()
	builds.Do(func() {
		pub, priv, _ := ed25519.GenerateKey(nil)
		releaseKey = priv
		dir, err := os.MkdirTemp("", "v2releases") //nolint:usetesting // the builds outlive this test: every update test shares them
		if err != nil {
			panic(err)
		}
		oldBin, newBin, otherBin = filepath.Join(dir, "old"), filepath.Join(dir, "new"), filepath.Join(dir, "other")
		for path, flags := range map[string]string{
			oldBin:   "-X main.version=v0.9.0 -X main.signingKey=" + base64.StdEncoding.EncodeToString(pub),
			newBin:   "-X main.version=v0.9.1",
			otherBin: "-X main.version=other-platform",
		} {
			if out, err := exec.Command("go", "build", "-ldflags", flags, "-o", path, "../cmd/v2").CombinedOutput(); err != nil {
				panic(string(out))
			}
		}
	})
}

// releaseFiles are what release.sh uploads besides the checksums: a Mac zip and bare Linux binaries.
var releaseFiles = []string{"v2-macos.zip", "v2-linux-amd64", "v2-linux-arm64"}

// thisPlatform is the file this machine's v2 must download.
func thisPlatform() string {
	if runtime.GOOS == "darwin" {
		return "v2-macos.zip"
	}
	return "v2-linux-" + runtime.GOARCH
}

// zipped is binary zipped as v2, the way release.sh zips it for the Mac.
func zipped(t *testing.T, binary []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	h := &zip.FileHeader{Name: "v2", Method: zip.Deflate}
	h.SetMode(0o755)
	w, err := zw.CreateHeader(h)
	must(t, err)
	_, err = w.Write(binary)
	must(t, errors.Join(err, zw.Close()))
	return b.Bytes()
}

// publish puts a release on the site the way release.sh does: every platform's file, their sha256s in
// one checksums.txt, and checksums.txt signed with key. binary goes in this platform's file; the
// others hold otherBin, which says it's another version.
func publish(t *testing.T, site *fakeSite, version string, binary []byte, key ed25519.PrivateKey) {
	t.Helper()
	other, _ := os.ReadFile(otherBin)
	site.latest = version
	site.release = map[string][]byte{}
	var sums []byte
	for _, name := range releaseFiles {
		b := other
		if name == thisPlatform() {
			b = binary
		}
		if strings.HasSuffix(name, ".zip") {
			b = zipped(t, b)
		}
		site.release[version+"/"+name] = b
		sums = fmt.Appendf(sums, "%x  %s\n", sha256.Sum256(b), name)
	}
	site.release[version+"/checksums.txt"] = sums
	site.release[version+"/checksums.txt.sig"] = ed25519.Sign(key, sums)
}

// installed is v0.9.0 installed the way install.sh leaves it, plus a link to it from another folder.
func installed(t *testing.T) (*fakeSite, []string, string, string) {
	t.Helper()
	buildReleases(t)
	site := &fakeSite{}
	srv := httptest.NewServer(site)
	t.Cleanup(srv.Close)
	site.url = srv.URL
	home := t.TempDir()
	installedBin := filepath.Join(home, ".local/bin/v2")
	link := filepath.Join(home, "bin/v2")
	must(t, os.MkdirAll(filepath.Dir(installedBin), 0o755))
	must(t, os.MkdirAll(filepath.Dir(link), 0o755))
	b, _ := os.ReadFile(oldBin)
	must(t, os.WriteFile(installedBin, b, 0o755))
	must(t, os.Symlink(installedBin, link))
	return site, []string{"HOME=" + home, "V2_SERVER=" + srv.URL}, installedBin, link
}

func run(t *testing.T, binary string, env []string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), env...)
	out, _ := cmd.CombinedOutput()
	return string(out), cmd.ProcessState.ExitCode()
}

func TestUpdateReplacesTheBinary(t *testing.T) {
	site, env, installedBin, link := installed(t)
	b, _ := os.ReadFile(newBin)
	publish(t, site, "v0.9.1", b, releaseKey)

	if out, code := run(t, link, env, "update"); code != 0 || !strings.Contains(out, "v0.9.0 -> v0.9.1") {
		t.Fatalf("update: exit %d: %s", code, out)
	}
	if out, _ := run(t, installedBin, env, "--version"); out != "v2 version v0.9.1\n" {
		t.Fatalf("after update: %q", out)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("update replaced the link instead of the binary it points to")
	}
	if entries, _ := os.ReadDir(filepath.Dir(installedBin)); len(entries) != 1 {
		t.Fatalf("left files next to v2: %v", entries)
	}
	if out, code := run(t, installedBin, env, "update"); code != 0 || !strings.Contains(out, "up to date") {
		t.Fatalf("second update: exit %d: %s", code, out)
	}
}

func TestUpdateRefusesABadReleaseAndKeepsTheOldBinary(t *testing.T) {
	buildReleases(t)
	newer, _ := os.ReadFile(newBin)
	_, otherKey, _ := ed25519.GenerateKey(nil)
	for _, c := range []struct {
		name, want string
		publish    func(site *fakeSite)
	}{
		{"tampered zip", "doesn't match its signed checksum", func(site *fakeSite) {
			evil := &fakeSite{}
			publish(t, evil, "v0.9.1", append(newer, "evil"...), releaseKey)
			publish(t, site, "v0.9.1", newer, releaseKey)
			for _, name := range releaseFiles {
				site.release["v0.9.1/"+name] = evil.release["v0.9.1/"+name]
			}
		}},
		{"signed by another key", "isn't signed by v2.video", func(site *fakeSite) {
			publish(t, site, "v0.9.1", newer, otherKey)
		}},
		{"binary of another version", "didn't run as v0.9.2", func(site *fakeSite) {
			publish(t, site, "v0.9.2", newer, releaseKey)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			site, env, installedBin, _ := installed(t)
			c.publish(site)
			before, _ := os.ReadFile(installedBin)
			out, code := run(t, installedBin, env, "update")
			if code != 1 || !strings.Contains(out, c.want) {
				t.Fatalf("exit %d: %s", code, out)
			}
			if after, _ := os.ReadFile(installedBin); !bytes.Equal(before, after) {
				t.Fatal("the old binary changed")
			}
			if entries, _ := os.ReadDir(filepath.Dir(installedBin)); len(entries) != 1 {
				t.Fatalf("left files next to v2: %v", entries)
			}
		})
	}
}

func TestUpdateNoticeAtMostOnceADay(t *testing.T) {
	site, env, installedBin, _ := installed(t)
	publish(t, site, "v0.9.1", []byte("unused"), releaseKey)
	notice := "v2 v0.9.1 is out (you have v0.9.0): run v2 update\n"
	if out, _ := run(t, installedBin, env, "log"); !strings.Contains(out, notice) {
		t.Fatalf("first command: %q", out)
	}
	if out, _ := run(t, installedBin, env, "log"); strings.Contains(out, "is out") {
		t.Fatalf("second command the same day: %q", out)
	}
	if n := strings.Count(strings.Join(site.calls, " "), "cli:latest"); n != 1 {
		t.Fatalf("asked for the latest version %d times, want 1", n)
	}
}

func TestTooOldIsRefusedInTheSitesWords(t *testing.T) {
	site, env := cloneSite(t)
	site.tooOld = true
	out, code := v2(t, t.TempDir(), env, "clone", "alice/demo")
	want := "This v2 (dev) is too old for v2.video. Update with: curl -fsSL https://v2.video/install.sh | sh"
	if code != 1 || !strings.Contains(out, want) {
		t.Fatalf("exit %d: %q", code, out)
	}
}
