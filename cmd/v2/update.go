package main

// v2 update and the once-a-day "new version" notice. Releases live in R2 under cli/<version>/ and
// are fetched through the Convex site's /cli/ redirects. checksums.txt is signed with ed25519; the
// download must match its signed sha256 before anything on disk changes.

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Agent-Native-Inc/v2-cli/internal/platform"
)

// signingKey is the release public key. Release builds never set it; the e2e tests link in their own
// key with -X, which only changes the binary they build.
var signingKey = "Yjs9xfruprB6/6SVFzclHFvS5zpMRbG59AXEZ1abYTQ="

type release struct{ Latest, Min string }

func latestRelease() (release, error) {
	var r release
	l := login{Server: envOr("V2_SERVER", defaultServer)}
	return r, l.call("query", "cli:latest", map[string]any{}, &r)
}

// semver turns "v1.2.3" into numbers; ok is false for anything else, like a "dev" build.
func semver(v string) ([3]int, bool) {
	var n [3]int
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) != 3 || !strings.HasPrefix(v, "v") {
		return n, false
	}
	for i, p := range parts {
		x, err := strconv.Atoi(p)
		if err != nil {
			return n, false
		}
		n[i] = x
	}
	return n, true
}

func newer(latest, current string) bool {
	a, okA := semver(latest)
	b, okB := semver(current)
	return okA && okB && slices.Compare(a[:], b[:]) > 0
}

// notifyUpdate prints a one-line notice when a newer v2 is out, checking at most once a day. It
// never fails the command: any error just means no notice today.
func notifyUpdate() {
	dir, err := os.UserConfigDir()
	if err != nil || version == "dev" {
		return
	}
	path := filepath.Join(dir, "v2", "update.json")
	var state struct {
		Checked time.Time `json:"Checked"`
	}
	if readJSON(path, &state) == nil && time.Since(state.Checked) < 24*time.Hour {
		return
	}
	state.Checked = time.Now()
	if b, err := json.Marshal(state); err == nil && os.MkdirAll(filepath.Dir(path), 0o755) == nil {
		_ = os.WriteFile(path, b, 0o644)
	}
	apiClient.Timeout = 2 * time.Second // runs before the command, which gets the usual minute back
	r, err := latestRelease()
	apiClient.Timeout = time.Minute
	if err == nil && newer(r.Latest, version) {
		fmt.Fprintf(os.Stderr, "v2 %s is out (you have %s): run v2 update\n", r.Latest, version)
	}
}

func runUpdate() error {
	r, err := latestRelease()
	if err != nil {
		return err
	}
	if !newer(r.Latest, version) {
		fmt.Printf("v2 %s is up to date (latest is %s)\n", version, r.Latest)
		return nil
	}
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return err
	}
	downloaded, err := fetchVerified(r.Latest)
	if err != nil {
		return err
	}
	bin, err := unpack(downloaded)
	if err != nil {
		return err
	}
	if err := replaceExecutable(exe, bin, r.Latest); err != nil {
		return err
	}
	fmt.Printf("updated v2 %s -> %s\n", version, r.Latest)
	return nil
}

// fetchVerified downloads this platform's file of release v once checksums.txt verifies against
// signingKey and the file matches its line there.
func fetchVerified(v string) ([]byte, error) {
	site := strings.Replace(envOr("V2_SERVER", defaultServer), ".convex.cloud", ".convex.site", 1) + "/cli/" + v + "/"
	sums, err := fetch(site + "checksums.txt")
	if err != nil {
		return nil, err
	}
	sig, err := fetch(site + "checksums.txt.sig")
	if err != nil {
		return nil, err
	}
	pub, _ := base64.StdEncoding.DecodeString(signingKey)
	if len(pub) != ed25519.PublicKeySize || !ed25519.Verify(pub, sums, sig) {
		return nil, fmt.Errorf("the %s release isn't signed by v2.video; nothing changed", v)
	}
	got, err := fetch(site + platform.ReleaseFile)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(got)
	if !bytes.Contains(sums, []byte(hex.EncodeToString(sum[:])+"  "+platform.ReleaseFile+"\n")) {
		return nil, fmt.Errorf("the downloaded %s doesn't match its signed checksum; nothing changed", platform.ReleaseFile)
	}
	return got, nil
}

// unpack returns the v2 binary in a release file: the v2 inside a zip, or the file itself.
func unpack(file []byte) ([]byte, error) {
	if !strings.HasSuffix(platform.ReleaseFile, ".zip") {
		return file, nil
	}
	zr, err := zip.NewReader(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		return nil, err
	}
	f, err := zr.Open("v2")
	if err != nil {
		return nil, fmt.Errorf("%s has no v2 inside: %w", platform.ReleaseFile, err)
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}

// replaceExecutable puts bin in exe's place only once it runs as version v, so a failure leaves exe as it was.
func replaceExecutable(exe string, bin []byte, v string) error {
	tmp, err := os.CreateTemp(filepath.Dir(exe), ".v2-update-*")
	if err != nil {
		return fmt.Errorf("can't write next to %s, so it can't be replaced: %w", exe, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, err = tmp.Write(bin)
	if err = errors.Join(err, tmp.Close(), os.Chmod(tmp.Name(), 0o755)); err != nil {
		return err
	}
	out, err := exec.Command(tmp.Name(), "--version").Output()
	if want := "v2 version " + v + "\n"; err != nil || string(out) != want {
		return fmt.Errorf("the new v2 didn't run as %s (%q, %v); nothing changed", v, out, err) //nolint:errorlint // err is nil when the output is wrong, and %w would print that as %!w(<nil>)
	}
	return os.Rename(tmp.Name(), exe)
}

// fetch downloads url whole. Like a blob download, it ends only when no bytes move for a while.
func fetch(url string) ([]byte, error) {
	ctx, moved, settle := watchStall()
	b, err := fetchUntil(ctx, moved, url)
	return b, settle(err)
}

func fetchUntil(ctx context.Context, moved io.Writer, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errUnreachable, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading %s: %s", url, res.Status)
	}
	var buf bytes.Buffer
	_, err = io.Copy(io.MultiWriter(&buf, moved), res.Body)
	return buf.Bytes(), err
}
