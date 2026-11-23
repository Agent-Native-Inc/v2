package e2e

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// recode turns from into to through JSON, as the site would read what v2 sent.
func recode(t *testing.T, from, to any) {
	t.Helper()
	b, err := json.Marshal(from)
	must(t, err)
	must(t, json.Unmarshal(b, to))
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// siteEnv runs v2 against the fake site at url, in a home of its own. login opens its link in the
// browser (open on the Mac, xdg-open on Linux); a stub first on PATH notes the link in opened instead.
func siteEnv(t *testing.T, url string) ([]string, string) {
	t.Helper()
	noBrowser := t.TempDir()
	opened := filepath.Join(noBrowser, "opened")
	for _, name := range []string{"open", "xdg-open"} {
		must(t, os.WriteFile(filepath.Join(noBrowser, name), []byte("#!/bin/sh\necho \"$1\" >> "+opened+"\n"), 0o755))
	}
	return append([]string{"PATH=" + noBrowser + ":" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "V2_SERVER=" + url, "V2_SITE=https://site.test"}, gitIdentity...), opened
}

// fakeSite stands in for v2.video's Convex functions (convex/cli.ts) over Convex's HTTP API, and for
// R2's signed part uploads and downloads.
type fakeSite struct {
	mu        sync.Mutex
	url       string
	uploads   map[string][]byte // sha256 -> bytes, completed by cli:r2Complete
	types     map[string]string // sha256 -> Content-Type cli:r2Start was given
	blobs     map[string]bool   // sha256 registered, each checked against its bytes
	pushes    []map[string]any
	approved  bool
	loggedOut bool
	parts     map[string]map[int][]byte // R2 upload id -> its parts so far
	starts    map[string]int            // sha256 -> times cli:r2Start was called
	ids       int
	hangBig   bool                // hold every full-size part after the first open, like a push about to be killed
	hung      chan struct{}       // gets a value when a part is held
	bigParts  int                 // full-size parts stored
	etagsOf   map[string][]string // sha256 -> etags it was completed with
	comments  []map[string]any
	resolved  []any
	// For v2 clone: what cli:clone returns, and the blobs it can download.
	project      map[string]any
	cloneArgs    map[string]any
	stored       map[string][]byte // sha256 -> bytes; R2 blobs come through a redirect, the rest in two parts
	r2           map[string]bool   // stored by cli:r2Complete
	redirects    int
	downloads    map[string]int
	failAfter    int    // refuse blob downloads after this many blobs, like a dropping connection
	corrupt      string // this sha256 is served with its bytes changed
	calls        []string
	refuse       bool     // reply to cli:push the way Convex rejects a request: {"code", "message"}
	limit        float64  // when set, cli:whoami says the account holds 0 bytes of this many
	stray        string   // cli:blobUrls also returns this hash, which wasn't asked for, with stored["stray"] as its bytes
	dropComplete string   // cut the connection on this sha256's next cli:r2Complete, like Wi-Fi dropping at the end of an upload
	pollFaults   []string // how the next cli:whoami calls fail while login waits: "drop" the connection, "cut" the reply short, "502" from a gateway, or "503" from an overloaded Convex
	pollError    string   // cli:whoami fails with this message
	revoked      bool     // cli:whoami knows no one by the old token, as after a logout on the site; a new login clears it
	// For v2 update: what cli:latest says, and the release files (version/file -> bytes) behind /cli/.
	latest     string
	release    map[string][]byte
	tooOld     bool // refuse every call but cli:latest, as the site does a v2 below CLI_MIN
	taken      bool // refuse a new project's cli:push: the account already has one of that name
	visibility map[string]string
}

const partSize = 64 << 20

func (f *fakeSite) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	if rest, ok := strings.CutPrefix(r.URL.Path, "/part/"); ok && r.Method == http.MethodPut {
		f.putPart(w, r, rest, body)
		return
	}
	if rest, ok := strings.CutPrefix(r.URL.Path, "/blob/"); ok {
		blob, part, inParts := strings.Cut(rest, "/")
		if !inParts {
			f.redirects++
			http.Redirect(w, r, f.url+"/r2/"+blob, http.StatusFound)
			return
		}
		f.download(w, blob, part == "0", func(b []byte) []byte {
			if part == "0" {
				return b[:len(b)/2]
			}
			return b[len(b)/2:]
		})
		return
	}
	if file, ok := strings.CutPrefix(r.URL.Path, "/cli/"); ok {
		http.Redirect(w, r, f.url+"/r2cli/"+file, http.StatusFound)
		return
	}
	if file, ok := strings.CutPrefix(r.URL.Path, "/r2cli/"); ok {
		if b, ok := f.release[file]; ok {
			_, _ = w.Write(b)
			return
		}
		http.NotFound(w, r)
		return
	}
	if blob, ok := strings.CutPrefix(r.URL.Path, "/r2/"); ok {
		f.download(w, blob, true, func(b []byte) []byte { return b })
		return
	}
	var req struct {
		Path string         `json:"path"`
		Args map[string]any `json:"args"`
	}
	_ = json.Unmarshal(body, &req) // a bad body leaves no version, which is refused below
	f.calls = append(f.calls, req.Path)
	v, _ := req.Args["version"].(string)
	if v == "" {
		http.Error(w, req.Path+" was called without the CLI's version", http.StatusBadRequest)
		return
	}
	if f.tooOld && req.Path != "cli:latest" {
		refuse(w, "This v2 ("+v+") is too old for v2.video. Update with: curl -fsSL https://v2.video/install.sh | sh")
		return
	}
	f.call(w, req.Path, req.Args)
}

// putPart stores one part of an R2 upload.
func (f *fakeSite) putPart(w http.ResponseWriter, r *http.Request, rest string, body []byte) {
	id, n, _ := strings.Cut(rest, "/")
	number, _ := strconv.Atoi(n)
	if f.parts[id] == nil {
		http.Error(w, "NoSuchUpload", http.StatusNotFound)
		return
	}
	if len(body) == partSize && f.hangBig && f.bigParts > 0 {
		f.mu.Unlock()
		f.hung <- struct{}{}
		<-r.Context().Done()
		f.mu.Lock()
		return
	}
	if len(body) == partSize {
		f.bigParts++
	}
	f.parts[id][number] = body
	w.Header().Set("ETag", etag(body))
}

func etag(part []byte) string {
	return fmt.Sprintf("%q", fmt.Sprintf("%x", sha256.Sum256(part))[:32])
}

// send writes v as the JSON answer.
func send(w http.ResponseWriter, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(append(b, '\n'))
}

// reply answers a Convex call with its value.
func reply(w http.ResponseWriter, v any) {
	send(w, map[string]any{"status": "success", "value": v})
}

// refuse answers a Convex call the way a ConvexError does: the message is in errorData.
func refuse(w http.ResponseWriter, msg string) {
	send(w, map[string]any{"status": "error", "errorMessage": "Server Error", "errorData": msg})
}

// drop cuts the connection without an answer.
func drop(w http.ResponseWriter) {
	if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
		_ = conn.Close()
	}
}

// call runs the Convex function fn.
func (f *fakeSite) call(w http.ResponseWriter, fn string, args map[string]any) {
	switch fn {
	case "cli:latest":
		reply(w, map[string]string{"latest": f.latest, "min": "v0.8.0"})
	case "cli:startLogin":
		f.approved = true // the user approves in the browser right away
		f.revoked = false
		reply(w, map[string]string{"code": "ABCD-2345", "url": "/cli/ABCD-2345"})
	case "cli:whoami":
		f.whoami(w)
	case "cli:missingBlobs":
		if len(args["hashes"].([]any)) > 200 {
			send(w, map[string]any{"status": "error", "errorMessage": "Too many reads in a single function execution"})
			return
		}
		missing := []string{}
		for _, h := range args["hashes"].([]any) {
			if !f.blobs[h.(string)] {
				missing = append(missing, h.(string))
			}
		}
		reply(w, missing)
	case "cli:r2Start":
		if f.parts == nil {
			f.parts, f.starts, f.types = map[string]map[int][]byte{}, map[string]int{}, map[string]string{}
		}
		sha := args["sha256"].(string)
		f.ids++
		id := fmt.Sprintf("u%d", f.ids)
		f.parts[id] = map[int][]byte{}
		f.starts[sha]++
		f.types[sha] = args["contentType"].(string)
		reply(w, map[string]string{"uploadId": id})
	case "cli:r2Parts":
		urls := []string{}
		for _, n := range args["parts"].([]any) {
			urls = append(urls, fmt.Sprintf("%s/part/%s/%g", f.url, args["uploadId"], n))
		}
		reply(w, map[string]any{"urls": urls})
	case "cli:r2Complete":
		f.r2Complete(w, args)
	case "cli:comments":
		reply(w, f.comments)
	case "cli:resolveComments":
		f.resolved = append(f.resolved, args["done"].([]any)...)
		reply(w, nil)
	case "cli:clone":
		f.cloneArgs = args
		reply(w, f.project)
	case "cli:blobUrls":
		f.blobURLs(w, args)
	case "cli:shareBlobs":
		reply(w, nil)
	case "cli:setVisibility":
		if f.visibility == nil {
			f.visibility = map[string]string{}
		}
		id, want := args["videoId"].(string), args["visibility"].(string)
		before := f.visibility[id]
		if before == "" {
			before = "private"
		}
		f.visibility[id] = want
		reply(w, map[string]any{"changed": before != want, "url": "/v/demo"})
	case "cli:logout":
		f.loggedOut = true
		reply(w, nil)
	case "cli:push":
		f.push(w, args)
	default:
		http.Error(w, fn, http.StatusNotFound)
	}
}

func (f *fakeSite) whoami(w http.ResponseWriter) {
	if len(f.pollFaults) > 0 {
		fault := f.pollFaults[0]
		f.pollFaults = f.pollFaults[1:]
		switch fault {
		case "502":
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, "<html><body>502 Bad Gateway</body></html>")
			return
		case "503":
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"code":"ServiceUnavailable","message":"Service temporarily unavailable"}`)
			return
		case "cut":
			w.Header().Set("Content-Length", "200")
			_, _ = io.WriteString(w, `{"status":"succ`)
			w.(http.Flusher).Flush()
		}
		drop(w)
		return
	}
	if f.pollError != "" {
		refuse(w, f.pollError)
		return
	}
	if f.revoked {
		reply(w, nil)
		return
	}
	who := map[string]any{"name": "Alice", "email": "a@example.com"}
	if f.limit > 0 {
		who["storedBytes"], who["limitBytes"] = 0.0, f.limit
	}
	reply(w, who)
}

func (f *fakeSite) r2Complete(w http.ResponseWriter, args map[string]any) {
	if f.dropComplete == args["sha256"] {
		f.dropComplete = ""
		drop(w)
		return
	}
	id, sha := args["uploadId"].(string), args["sha256"].(string)
	var all []byte
	for i, e := range args["etags"].([]any) {
		part := f.parts[id][i+1]
		if e != etag(part) {
			refuse(w, fmt.Sprintf("part %d's etag doesn't match", i+1))
			return
		}
		all = append(all, part...)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(all)); got != sha || float64(len(all)) != args["size"] {
		refuse(w, "bytes don't match "+got)
		return
	}
	delete(f.parts, id)
	f.blobs[sha] = true
	f.uploads[sha] = all
	if f.stored == nil {
		f.stored, f.r2, f.etagsOf = map[string][]byte{}, map[string]bool{}, map[string][]string{}
	}
	f.stored[sha], f.r2[sha] = all, true
	for _, e := range args["etags"].([]any) {
		f.etagsOf[sha] = append(f.etagsOf[sha], e.(string))
	}
	reply(w, nil)
}

func (f *fakeSite) blobURLs(w http.ResponseWriter, args map[string]any) {
	found := []map[string]any{}
	for _, h := range args["hashes"].([]any) {
		urls := []string{f.url + "/blob/" + h.(string) + "/0", f.url + "/blob/" + h.(string) + "/1"}
		if f.r2[h.(string)] {
			urls = []string{f.url + "/blob/" + h.(string)}
		}
		if data, ok := f.stored[h.(string)]; ok {
			found = append(found, map[string]any{"sha256": h, "size": len(data), "urls": urls})
		}
	}
	if f.stray != "" {
		found = append(found, map[string]any{"sha256": f.stray, "size": len(f.stored["stray"]), "urls": []string{f.url + "/blob/stray/0", f.url + "/blob/stray/1"}})
	}
	reply(w, found)
}

func (f *fakeSite) push(w http.ResponseWriter, args map[string]any) {
	if f.refuse {
		w.WriteHeader(http.StatusBadRequest)
		send(w, map[string]string{"code": "BadJsonBody", "message": "the request was too large"})
		return
	}
	if f.taken && args["videoId"] == nil {
		refuse(w, fmt.Sprintf("You already have a project called %q: /alice/demo\nNothing was pushed.", args["title"]))
		return
	}
	for _, ver := range args["versions"].([]any) {
		ver := ver.(map[string]any)
		hashes := []any{ver["bundle"], ver["poster"], ver["preview"]}
		for _, file := range ver["files"].([]any) {
			hashes = append(hashes, file.(map[string]any)["sha256"])
		}
		for _, h := range hashes {
			if h, _ := h.(string); h != "" && !f.blobs[h] {
				refuse(w, fmt.Sprintf("V%v needs blobs you haven't uploaded: %s", ver["n"], h))
				return
			}
		}
	}
	f.pushes = append(f.pushes, args)
	reply(w, map[string]any{"videoId": "vid1", "url": "/v/demo", "pushed": json.RawMessage(`[1.0, 2.0]`)})
}

// download serves a stored blob, or the piece of it that cut picks; first is the request that starts it.
func (f *fakeSite) download(w http.ResponseWriter, blob string, first bool, cut func([]byte) []byte) {
	data, begun := f.stored[blob], f.downloads[blob] > 0
	if first && !begun && f.failAfter > 0 && len(f.downloads) >= f.failAfter {
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	if blob == f.corrupt {
		data = append([]byte{data[0] ^ 1}, data[1:]...)
	}
	if first {
		if f.downloads == nil {
			f.downloads = map[string]int{}
		}
		f.downloads[blob]++
	}
	_, _ = w.Write(cut(data))
}

func TestPushSendsEveryVersionWithItsMedia(t *testing.T) {
	site := &fakeSite{uploads: map[string][]byte{}, blobs: map[string]bool{}}
	srv := httptest.NewServer(site)
	defer srv.Close()
	site.url = srv.URL
	env, opened := siteEnv(t, srv.URL)

	proj := t.TempDir()
	newProject(t, proj)
	if out, code := v2(t, proj, env, "push"); code != 1 || !strings.Contains(out, "v2 login") {
		t.Fatalf("push before login: exit %d: %s", code, out)
	}
	out, code := v2(t, proj, env, "login")
	if code != 0 || !strings.Contains(out, "https://site.test/cli/ABCD-2345") || !strings.Contains(out, "Alice") {
		t.Fatalf("login: exit %d: %s", code, out)
	}
	if got := waitOpened(t, opened); got != "https://site.test/cli/ABCD-2345\n" {
		t.Fatalf("login opened %q", got)
	}

	must(t, os.WriteFile(filepath.Join(proj, ".v2/config.json"), []byte(`{"render": "out/final.mp4"}`), 0o644))
	must(t, os.MkdirAll(filepath.Join(proj, "out"), 0o755))
	if b, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc=size=1920x1080:rate=10:duration=1",
		"-pix_fmt", "yuv420p", filepath.Join(proj, "out/final.mp4")).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %s", b)
	}
	must(t, os.WriteFile(filepath.Join(proj, "README.md"), []byte("# Launch film\n\nmade with remotion\n"), 0o644))
	save(t, proj, "-m", "first cut", "--prompt", "make a launch film", "--model", "claude-opus-5-5", "--change", "first version")
	must(t, os.WriteFile(filepath.Join(proj, "src/a.txt"), []byte("x"), 0o644))
	save(t, proj, "-m", "shorter", "--change", "cut 0:00.5 2s slow intro")

	// A small 1080p render plays as it is, so push makes no copy of it.
	if out, code := v2(t, proj, env, "push"); code != 0 || !strings.Contains(out, "https://site.test/v/demo") || strings.Contains(out, "1080p copy") {
		t.Fatalf("push: exit %d: %s", code, out)
	}
	checkFirstPush(t, site)
	checkMediaTypes(t, site)
	starts := len(site.starts)
	if out, code := v2(t, proj, env, "push"); code != 0 {
		t.Fatalf("second push: exit %d: %s", code, out)
	}
	if len(site.starts) != starts {
		t.Fatalf("second push uploaded %d files again", len(site.starts)-starts)
	}
	if site.pushes[1]["videoId"] != "vid1" {
		t.Fatalf("second push went to a new video: %v", site.pushes[1]["videoId"])
	}

	if out, code := v2(t, proj, env, "logout"); code != 0 || !site.loggedOut {
		t.Fatalf("logout: exit %d, server told: %v: %s", code, site.loggedOut, out)
	}
	if out, code := v2(t, proj, env, "push"); code != 1 || !strings.Contains(out, "v2 login") {
		t.Fatalf("push after logout: exit %d: %s", code, out)
	}
}

// checkFirstPush checks what TestPushSendsEveryVersionWithItsMedia's first push sent: both versions
// with their details and code, each pointing only at uploaded blobs.
func checkFirstPush(t *testing.T, site *fakeSite) {
	t.Helper()
	var push struct {
		Title    string
		VideoID  string
		Versions []struct {
			N                      int
			Bundle, Title, Model   string
			Prompts                []string
			Readme, Render, Poster string
			Preview                string
			Duration               float64
			Width                  int
			Files                  []struct{ Path, Sha256, Source string }
			Changes                []struct {
				Kind    string
				Removed float64
			}
		}
	}
	recode(t, site.pushes[0], &push)
	if push.Title != "Launch film" || push.VideoID != "" || len(push.Versions) != 2 {
		t.Fatalf("push: %+v", site.pushes[0])
	}
	v1, ver2 := push.Versions[0], push.Versions[1]
	if v1.Title != "first cut" || v1.Model != "claude-opus-5-5" || len(v1.Prompts) != 1 || v1.Prompts[0] != "make a launch film" ||
		!strings.Contains(v1.Readme, "made with remotion") || v1.Render != "out/final.mp4" || v1.Width != 1920 || v1.Duration < 0.9 {
		t.Fatalf("v1: %+v", v1)
	}
	if len(ver2.Changes) != 1 || ver2.Changes[0].Kind != "cut" || ver2.Changes[0].Removed != 2 {
		t.Fatalf("v2 changes: %+v", ver2.Changes)
	}
	codeIn := func(n int) map[string]string {
		got := map[string]string{}
		for _, f := range push.Versions[n].Files {
			if f.Source == "code" {
				got[f.Path] = f.Sha256
			}
		}
		return got
	}
	if _, ok := codeIn(0)["src/a.txt"]; ok {
		t.Fatalf("v1 has a file saved in v2: %v", codeIn(0))
	}
	if codeIn(1)["src/a.txt"] != fmt.Sprintf("%x", sha256.Sum256([]byte("x"))) || codeIn(1)["README.md"] == "" {
		t.Fatalf("v2's code: %v", codeIn(1))
	}
	for path := range codeIn(1) {
		if strings.HasPrefix(path, ".v2/") || strings.HasPrefix(path, "out/") {
			t.Fatalf("%s isn't code", path)
		}
	}
	// Everything a version points at must be on the site, byte for byte (addBlob checked the hashes).
	for _, v := range push.Versions {
		if v.Preview != "" {
			t.Fatalf("v%d has a copy of a small 1080p render", v.N)
		}
		need := make([]string, 0, 2+len(v.Files))
		need = append(need, v.Bundle, v.Poster)
		for _, f := range v.Files {
			need = append(need, f.Sha256)
		}
		for _, h := range need {
			if !site.blobs[h] {
				t.Fatalf("v%d points at %q, which wasn't uploaded", v.N, h)
			}
		}
	}
}

// waitOpened returns the line login opened in the browser. login doesn't wait for `open`, and macOS
// vets the new stub script before it first runs, so the line can land seconds after login exits.
// Wait for the whole line, up to 30 s (about 10x the slowest first run seen).
func waitOpened(t *testing.T, opened string) string {
	t.Helper()
	var got []byte
	for deadline := time.Now().Add(30 * time.Second); !bytes.HasSuffix(got, []byte("\n")); {
		if time.Now().After(deadline) {
			t.Fatalf("login never ran open: %q", got)
		}
		time.Sleep(20 * time.Millisecond)
		got, _ = os.ReadFile(opened)
	}
	return string(got)
}

// checkMediaTypes fails unless a JPEG poster went up and every render went up as video/mp4.
func checkMediaTypes(t *testing.T, site *fakeSite) {
	t.Helper()
	jpegs := 0
	for id, data := range site.uploads {
		if bytes.HasPrefix(data, []byte{0xFF, 0xD8}) {
			jpegs++
		}
		// Chrome won't stream a render stored as application/octet-stream (ORB blocks its range requests).
		if len(data) > 8 && string(data[4:8]) == "ftyp" && site.types[id] != "video/mp4" {
			t.Fatalf("render uploaded as %q, want video/mp4", site.types[id])
		}
	}
	if jpegs == 0 {
		t.Fatal("no JPEG poster was uploaded")
	}
}

// killedPush starts a push and kills it while the site holds open its second full-size part.
func killedPush(t *testing.T, site *fakeSite, proj string, env []string) {
	t.Helper()
	site.hangBig, site.hung = true, make(chan struct{})
	cmd := exec.Command(bin, "push")
	cmd.Dir, cmd.Env = proj, append(os.Environ(), env...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-site.hung:
	case <-time.After(time.Minute):
		t.Fatal("push never reached its second part")
	}
	must(t, cmd.Process.Kill())
	_ = cmd.Wait() // it was killed, so it reports that
	site.mu.Lock()
	site.hangBig = false
	site.mu.Unlock()
}

func TestPushResumesAfterAKilledPush(t *testing.T) {
	site, env := cloneSite(t)
	proj := t.TempDir()
	newProject(t, proj)
	big := writeRandom(t, filepath.Join(proj, "assets/generated/broll.mp4"), 2*partSize+8<<20)
	save(t, proj, "-m", "b-roll", "--change", "added b-roll")
	sum := fmt.Sprintf("%x", sha256.Sum256(big))

	killedPush(t, site, proj, env)
	if site.blobs[sum] || site.bigParts != 1 {
		t.Fatalf("after the kill: registered %v, full parts stored %d (want false, 1)", site.blobs[sum], site.bigParts)
	}
	first := fmt.Sprintf("%q", fmt.Sprintf("%x", sha256.Sum256(big[:partSize]))[:32])
	out, code := v2(t, proj, env, "push")
	if code != 0 {
		t.Fatalf("second push: exit %d: %s", code, out)
	}
	if len(out) > 500 {
		t.Fatalf("progress floods an agent's output (%d bytes):\n%.500s", len(out), out)
	}
	if !site.blobs[sum] || site.starts[sum] != 1 || site.bigParts != 2 || len(site.etagsOf[sum]) != 3 || site.etagsOf[sum][0] != first {
		t.Fatalf("resume: registered %v, started %d times (want 1), full parts stored %d (want 2), etags %v (first should be %s)",
			site.blobs[sum], site.starts[sum], site.bigParts, site.etagsOf[sum], first)
	}
	if got := read(t, filepath.Join(proj, ".git/v2-uploads.json")); got != "{\n  \"r2\": {}\n}\n" {
		t.Fatalf("finished uploads are still listed: %s", got)
	}

	// R2 drops an unfinished upload after a week; the push after that starts the file over.
	big = writeRandom(t, filepath.Join(proj, "assets/generated/broll.mp4"), 2*partSize+8<<20)
	save(t, proj, "-m", "new b-roll", "--change", "new b-roll")
	sum = fmt.Sprintf("%x", sha256.Sum256(big))
	killedPush(t, site, proj, env)
	site.parts = map[string]map[int][]byte{}
	if out, code := v2(t, proj, env, "push"); code != 1 || !strings.Contains(out, "start it over") {
		t.Fatalf("push of an expired upload: exit %d: %s", code, out)
	}
	if out, code := v2(t, proj, env, "push"); code != 0 || !site.blobs[sum] || site.starts[sum] != 2 {
		t.Fatalf("push after it: exit %d, registered %v, started %d times: %s", code, site.blobs[sum], site.starts[sum], out)
	}

	// A connection lost while the site joins the parts keeps the upload, so the next push sends no part again.
	big = writeRandom(t, filepath.Join(proj, "assets/generated/broll.mp4"), 2*partSize+8<<20)
	save(t, proj, "-m", "third b-roll", "--change", "third b-roll")
	sum = fmt.Sprintf("%x", sha256.Sum256(big))
	site.dropComplete = sum
	if out, code := v2(t, proj, env, "push"); code != 1 || !strings.Contains(out, "can't reach") {
		t.Fatalf("push with a dropped connection: exit %d: %s", code, out)
	}
	parts := site.bigParts
	if out, code := v2(t, proj, env, "push"); code != 0 || !site.blobs[sum] || site.starts[sum] != 1 || site.bigParts != parts {
		t.Fatalf("push after the drop: exit %d, registered %v, started %d times (want 1), parts sent again %d: %s", code, site.blobs[sum], site.starts[sum], site.bigParts-parts, out)
	}
}

// The site plays the original; a render taller than 1080p, or above 8 Mbit/s, also gets a 1080p copy.
func TestPushMakesA1080pCopyOfABigRender(t *testing.T) {
	site, env := cloneSite(t)
	proj := t.TempDir()
	newProject(t, proj)
	must(t, os.WriteFile(filepath.Join(proj, ".v2/config.json"), []byte(`{"render": "out/final.mp4"}`), 0o644))
	must(t, os.MkdirAll(filepath.Join(proj, "out"), 0o755))
	render := func(args ...string) {
		args = append([]string{"-v", "error", "-y", "-f", "lavfi"}, append(args, "-pix_fmt", "yuv420p", filepath.Join(proj, "out/final.mp4"))...)
		if b, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg: %s", b)
		}
	}
	render("-i", "testsrc=size=2560x1440:rate=10:duration=1")
	save(t, proj, "-m", "1440p", "--change", "first version")
	render("-i", "testsrc=size=1920x1080:rate=10:duration=1", "-vf", "noise=alls=100:allf=t", "-crf", "5")
	save(t, proj, "-m", "noisy", "--change", "second version")

	out, code := v2(t, proj, env, "push")
	if code != 0 || strings.Count(out, "1080p copy") != 2 {
		t.Fatalf("push: exit %d: %s", code, out)
	}
	var push struct {
		Versions []struct{ Preview string }
	}
	recode(t, site.pushes[0], &push)
	for i, v := range push.Versions {
		data := site.uploads[v.Preview]
		mp4 := filepath.Join(t.TempDir(), "copy.mp4")
		must(t, os.WriteFile(mp4, data, 0o644))
		out, _ := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=height", "-of", "csv=p=0", mp4).Output()
		if height, _ := strconv.Atoi(strings.TrimSpace(string(out))); site.types[v.Preview] != "video/mp4" || height != 1080 {
			t.Fatalf("v%d's copy uploaded as %q, %dpx tall; want a 1080px video/mp4", i+1, site.types[v.Preview], height)
		}
	}
	if out, code := v2(t, proj, env, "push"); code != 0 || strings.Contains(out, "1080p copy") {
		t.Fatalf("second push encoded again: exit %d: %s", code, out)
	}
}

// ffmpegWithout puts an ffmpeg first on v2's PATH that lacks the named encoders, the way Fedora's own
// ffmpeg (ffmpeg-free) lacks libx264: -encoders doesn't list them, and an encode with one fails.
func ffmpegWithout(t *testing.T, env []string, encoders ...string) []string {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	dir, names := t.TempDir(), strings.Join(encoders, "|")
	must(t, os.WriteFile(filepath.Join(dir, "ffmpeg"), []byte(`#!/bin/sh
for a; do case "$a" in
-encoders) "`+ffmpeg+`" "$@" | grep -v -E ' (`+names+`) '; exit ;;
`+names+`) echo "Unknown encoder '$a'" >&2; exit 8 ;;
esac; done
exec "`+ffmpeg+`" "$@"
`), 0o755))
	for _, e := range env {
		if path, ok := strings.CutPrefix(e, "PATH="); ok {
			return append(env, "PATH="+dir+":"+path)
		}
	}
	t.Fatal("env has no PATH")
	return nil
}

// twoBigRenders is a project with two versions, each a 1440p render.
func twoBigRenders(t *testing.T) string {
	t.Helper()
	proj := t.TempDir()
	newProject(t, proj)
	must(t, os.WriteFile(filepath.Join(proj, ".v2/config.json"), []byte(`{"render": "out/final.mp4"}`), 0o644))
	must(t, os.MkdirAll(filepath.Join(proj, "out"), 0o755))
	for _, src := range []string{"testsrc", "testsrc2"} {
		if b, err := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", src+"=size=2560x1440:rate=10:duration=1",
			"-pix_fmt", "yuv420p", filepath.Join(proj, "out/final.mp4")).CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg: %s", b)
		}
		save(t, proj, "-m", src, "--change", "a version")
	}
	return proj
}

// Fedora's own ffmpeg has no libx264. Its H.264 encoder is libopenh264, so the copy is made with that.
func TestPushMakesThe1080pCopyWithoutLibx264(t *testing.T) {
	if out, _ := exec.Command("ffmpeg", "-v", "error", "-encoders").Output(); !strings.Contains(string(out), " libopenh264 ") {
		t.Skip("this ffmpeg has no libopenh264")
	}
	site, env := cloneSite(t)
	out, code := v2(t, twoBigRenders(t), ffmpegWithout(t, env, "libx264"), "push")
	if code != 0 || strings.Count(out, "making a 1080p copy") != 2 {
		t.Fatalf("push: exit %d: %s", code, out)
	}
	var push struct {
		Versions []struct{ Preview string }
	}
	recode(t, site.pushes[0], &push)
	for i, v := range push.Versions {
		mp4 := filepath.Join(t.TempDir(), "copy.mp4")
		must(t, os.WriteFile(mp4, site.uploads[v.Preview], 0o644))
		probe, _ := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=codec_name,height,pix_fmt", "-of", "csv=p=0", mp4).Output()
		if got := strings.TrimSpace(string(probe)); site.types[v.Preview] != "video/mp4" || got != "h264,1080,yuv420p" {
			t.Fatalf("v%d's copy uploaded as %q, %q; want a video/mp4 that is h264,1080,yuv420p\npush said: %s", i+1, site.types[v.Preview], got, out)
		}
	}
}

// With no H.264 encoder at all the push still goes through, and says once that the copies are missing and why.
func TestPushSaysWhyItMadeNo1080pCopy(t *testing.T) {
	site, env := cloneSite(t)
	out, code := v2(t, twoBigRenders(t), ffmpegWithout(t, env, "libx264", "libopenh264"), "push")
	if code != 0 || strings.Count(out, "no 1080p copy") != 1 || !strings.Contains(out, "H.264 encoder") || strings.Contains(out, "making a 1080p copy") {
		t.Fatalf("push: exit %d: %s", code, out)
	}
	if b, err := json.Marshal(site.pushes[0]); err != nil || strings.Contains(string(b), `"preview"`) {
		t.Fatalf("pushed a 1080p copy: %s", b)
	}
}

// Fedora's noopenh264 stub: ffmpeg lists libopenh264, but every encode with it fails. The push tries
// each version's copy and says once why there is none, and only the first try says it is making one.
func TestPushSaysOnceThatABrokenEncoderMadeNo1080pCopy(t *testing.T) {
	site, env := cloneSite(t)
	env = ffmpegWithout(t, env, "libx264", "libopenh264")
	path := ""
	for _, e := range env {
		if p, ok := strings.CutPrefix(e, "PATH="); ok {
			path = p
		}
	}
	under := filepath.Join(strings.Split(path, ":")[0], "ffmpeg")
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, "ffmpeg"), []byte(`#!/bin/sh
case " $* " in *" -encoders "*) "`+under+`" "$@"; echo " V....D libopenh264          OpenH264 H.264 / AVC / MPEG-4 AVC / MPEG-4 part 10 (codec h264)"; exit ;; esac
exec "`+under+`" "$@"
`), 0o755))
	env = append(env, "PATH="+dir+":"+path)
	out, code := v2(t, twoBigRenders(t), env, "push")
	if code != 0 || strings.Count(out, "making a 1080p copy") != 1 || strings.Count(out, "no 1080p copy") != 1 {
		t.Fatalf("push: exit %d: %s", code, out)
	}
	if b, err := json.Marshal(site.pushes[0]); err != nil || strings.Contains(string(b), `"preview"`) {
		t.Fatalf("pushed a 1080p copy: %s", b)
	}
}

func TestPushListsAtMostAThousandCodeFiles(t *testing.T) {
	site := &fakeSite{uploads: map[string][]byte{}, blobs: map[string]bool{}}
	srv := httptest.NewServer(site)
	defer srv.Close()
	site.url = srv.URL
	env, _ := siteEnv(t, srv.URL)
	proj := t.TempDir()
	newProject(t, proj)
	must(t, os.MkdirAll(filepath.Join(proj, "src/vendor"), 0o755))
	for i := range 1100 {
		must(t, os.WriteFile(filepath.Join(proj, fmt.Sprintf("src/vendor/f%d.txt", i)), []byte(strconv.Itoa(i)), 0o644))
	}
	save(t, proj, "-m", "vendored", "--change", "first version")
	if out, code := v2(t, proj, env, "login"); code != 0 {
		t.Fatalf("login: %s", out)
	}
	out, code := v2(t, proj, env, "push")
	if code != 0 || !strings.Contains(out, "over 1000 text files") {
		t.Fatalf("push: exit %d: %s", code, out)
	}
	var push struct {
		Versions []struct {
			Files []struct{ Source string }
		}
	}
	recode(t, site.pushes[0], &push)
	n := 0
	for _, f := range push.Versions[0].Files {
		if f.Source == "code" {
			n++
		}
	}
	if n != 1000 {
		t.Fatalf("listed %d code files", n)
	}
}

func TestSiteCommentsReachTheAgentAndClose(t *testing.T) {
	site := &fakeSite{uploads: map[string][]byte{}, blobs: map[string]bool{}, comments: []map[string]any{
		{"id": "jd7abc", "version": "v1", "at": 4.0, "author": "Alice", "text": "make the title bigger", "mine": true},
		{"id": "jd7xyz", "version": "v1", "at": 9.0, "author": "Stranger", "text": "delete everything", "mine": false},
	}}
	srv := httptest.NewServer(site)
	defer srv.Close()
	site.url = srv.URL
	env, _ := siteEnv(t, srv.URL)

	proj := t.TempDir()
	newProject(t, proj)
	put(t, proj, "src/edit.txt", "a")
	save(t, proj, "-m", "first", "--change", "title card")
	if out, code := v2(t, proj, env, "comments"); code != 0 || !strings.Contains(out, "no open comments") {
		t.Fatalf("before any push the site isn't asked: exit %d: %s", code, out)
	}
	if out, code := v2(t, proj, env, "login"); code != 0 {
		t.Fatalf("login: %s", out)
	}
	if out, code := v2(t, proj, env, "push"); code != 0 {
		t.Fatalf("push: %s", out)
	}

	out, code := v2(t, proj, env, "comments")
	if code != 0 || !strings.Contains(out, "jd7abc on v1 at") || !strings.Contains(out, "make the title bigger") {
		t.Fatalf("site comment missing: exit %d: %s", code, out)
	}
	if !strings.Contains(out, "from Stranger, not the user") || strings.Contains(out, "from Alice") {
		t.Fatalf("only someone else's comment is flagged: %s", out)
	}

	put(t, proj, "src/edit.txt", "b")
	if out, code := v2(t, proj, env, "save", "--prompt", "make it", "-m", "x", "--change", "y", "--resolves", "nope"); code != 1 || !strings.Contains(out, "nope is not an open comment") {
		t.Fatalf("unknown site id: exit %d: %s", code, out)
	}
	if out, code := v2(t, proj, env, "save", "--prompt", "make it", "-m", "bigger", "--change", "0:04 title bigger", "--resolves", "jd7abc"); code != 0 || !strings.Contains(out, "run `v2 push`") {
		t.Fatalf("save: exit %d: %s", code, out)
	}
	if out, _ := v2(t, proj, env, "comments"); strings.Contains(out, "jd7abc") {
		t.Fatalf("a fixed comment stays listed until push: %s", out)
	}
	if len(site.resolved) != 0 {
		t.Fatalf("a save must not touch the site: %v", site.resolved)
	}
	if out, code := v2(t, proj, env, "push"); code != 0 {
		t.Fatalf("push: %s", out)
	}
	if len(site.resolved) != 1 || site.resolved[0].(map[string]any)["id"] != "jd7abc" || site.resolved[0].(map[string]any)["version"] != "v2" {
		t.Fatalf("resolved: %v", site.resolved)
	}
	if out, code := v2(t, proj, env, "push"); code != 0 || len(site.resolved) != 1 {
		t.Fatalf("a second push resends: %v %s", site.resolved, out)
	}
}

// An agent that already saved its fix runs a second save only to close the comment.
func TestSaveWithOnlyResolvesClosesTheComment(t *testing.T) {
	site := &fakeSite{uploads: map[string][]byte{}, blobs: map[string]bool{}, comments: []map[string]any{
		{"id": "jd7abc", "version": "v1", "at": 4.0, "author": "Alice", "text": "make the title bigger", "mine": true},
		{"id": "jd7new", "version": "v2", "at": 2.0, "author": "Alice", "text": "now it is too big", "mine": true},
	}}
	srv := httptest.NewServer(site)
	defer srv.Close()
	site.url = srv.URL
	env, _ := siteEnv(t, srv.URL)

	proj := t.TempDir()
	newProject(t, proj)
	put(t, proj, "src/edit.txt", "a")
	save(t, proj, "-m", "first", "--change", "title card")
	if out, code := v2(t, proj, env, "login"); code != 0 {
		t.Fatalf("login: %s", out)
	}
	put(t, proj, "src/edit.txt", "b")
	save(t, proj, "-m", "bigger", "--change", "0:04 title bigger")
	if out, code := v2(t, proj, env, "push"); code != 0 {
		t.Fatalf("push: %s", out)
	}

	fixed := []string{"save", "--prompt", "fix my comment", "-m", "bigger", "--change", "0:04 title bigger", "--resolves"}
	out, code := v2(t, proj, env, append(fixed, "jd7abc")...)
	if code != 0 || !strings.Contains(out, "jd7abc") || !strings.Contains(out, "fixed in v2") || !strings.Contains(out, "run `v2 push` now") {
		t.Fatalf("a save with only --resolves: exit %d: %s", code, out)
	}
	if tags := git(t, proj, "tag", "--list"); tags != "v1\nv2\n" {
		t.Fatalf("a save that changed no file made a version: %q", tags)
	}
	if out, _ := v2(t, proj, env, "comments"); strings.Contains(out, "jd7abc") {
		t.Fatalf("the fixed comment is still listed: %s", out)
	}
	if out, code := v2(t, proj, env, "push"); code != 0 {
		t.Fatalf("push: %s", out)
	}
	if len(site.resolved) != 1 || site.resolved[0].(map[string]any)["id"] != "jd7abc" || site.resolved[0].(map[string]any)["version"] != "v2" {
		t.Fatalf("resolved: %v", site.resolved)
	}

	// No version came after v2, so none can have fixed a comment left on it.
	out, code = v2(t, proj, env, append(fixed, "jd7new")...)
	if code != 1 || !strings.Contains(out, "jd7new was left on v2") || exists(filepath.Join(proj, ".v2/comments/site-done.json")) {
		t.Fatalf("a comment on the last version, nothing changed: exit %d: %s", code, out)
	}

	// A comment from the v2 diff page needs nothing from the site.
	put(t, proj, ".v2/comments/comments.json", `[{"id":"c1","version":"v1","at":1,"text":"title is late","created_at":"x"}]`)
	site.calls = nil
	if out, code := v2(t, proj, env, append(fixed, "c1")...); code != 0 || slices.Contains(site.calls, "cli:comments") {
		t.Fatalf("a local comment, nothing changed: exit %d, site calls %v: %s", code, site.calls, out)
	}

	// Offline, save can't see which version a site comment was left on, so it marks nothing.
	srv.Close()
	out, code = v2(t, proj, env, append(fixed, "jd7new")...)
	if code != 1 || !strings.Contains(out, "jd7new") || !strings.Contains(out, "online") || exists(filepath.Join(proj, ".v2/comments/site-done.json")) {
		t.Fatalf("offline, a comment on the last version, nothing changed: exit %d: %s", code, out)
	}
}

func TestSaveRefreshesAgentInstructions(t *testing.T) {
	proj := t.TempDir()
	newProject(t, proj)
	path := filepath.Join(proj, "AGENTS.md")
	b, _ := os.ReadFile(path)
	stale := strings.ReplaceAll(string(b), "`v2 comments`", "`old command`")
	must(t, os.WriteFile(path, []byte("my notes\n"+stale), 0o644))
	put(t, proj, "src/edit.txt", "a")
	save(t, proj, "-m", "first", "--change", "title card")
	got, _ := os.ReadFile(path)
	if strings.Contains(string(got), "old command") || !strings.Contains(string(got), "my notes") {
		t.Fatalf("AGENTS.md: %s", got)
	}
}

func TestPushShowsTheSitesRefusal(t *testing.T) {
	site, env := cloneSite(t)
	site.refuse = true
	proj := t.TempDir()
	newProject(t, proj)
	put(t, proj, "src/edit.txt", "a")
	save(t, proj, "-m", "one", "--change", "one")
	if out, code := v2(t, proj, env, "push"); code != 1 || !strings.Contains(out, "the request was too large") {
		t.Fatalf("exit %d: %q", code, out)
	}
}

func TestPushUploadsNothingUnderATakenName(t *testing.T) {
	site, env := cloneSite(t)
	site.taken = true
	proj := t.TempDir()
	newProject(t, proj)
	put(t, proj, "src/edit.txt", "a")
	save(t, proj, "-m", "one", "--change", "one")
	out, code := v2(t, proj, env, "push")
	if code != 1 || !strings.Contains(out, "You already have a project called") {
		t.Fatalf("exit %d: %q", code, out)
	}
	if n := strings.Count(strings.Join(site.calls, " "), "cli:r2Start"); n != 0 || len(site.uploads) != 0 || strings.Contains(out, "uploading") {
		t.Fatalf("%d uploads started, %d finished before the refusal: %q", n, len(site.uploads), out)
	}
}

func TestPushRefusesWhatTheAccountHasNoRoomFor(t *testing.T) {
	site, env := cloneSite(t)
	proj := t.TempDir()
	newProject(t, proj)
	writeRandom(t, filepath.Join(proj, "assets/generated/clip.mp4"), 1<<20)
	save(t, proj, "-m", "one", "--change", "one")
	site.limit = 1 << 19
	out, code := v2(t, proj, env, "push")
	if code != 1 || !strings.Contains(out, "doesn't fit") || len(site.uploads) != 0 {
		t.Fatalf("exit %d, %d uploads: %s", code, len(site.uploads), out)
	}
	site.limit = 1 << 30
	if out, code := v2(t, proj, env, "push"); code != 0 {
		t.Fatalf("push with room: %s", out)
	}
}

// Offline, logout still signs this computer out: the token must not stay on disk.
func TestLogoutOfflineForgetsTheLogin(t *testing.T) {
	_, env := cloneSite(t)
	env = append(env, "V2_SERVER=http://127.0.0.1:1")
	var file string
	for _, e := range env {
		if home, ok := strings.CutPrefix(e, "HOME="); ok {
			t.Setenv("HOME", home) // where v2 keeps the login depends on the OS; ask Go the same way v2 does
			dir, _ := os.UserConfigDir()
			file = filepath.Join(dir, "v2/login.json")
		}
	}
	b := strings.Replace(read(t, file), `"server":"http://127.0.0.1:`, `"server":"http://127.0.0.1:1#`, 1)
	must(t, os.WriteFile(file, []byte(b), 0o600))
	out, _ := v2(t, t.TempDir(), env, "logout")
	if exists(file) {
		t.Fatalf("login.json kept after logout: %s", out)
	}
}

// newSite is a fake site and the env to reach it, with nobody signed in.
func newSite(t *testing.T) (*fakeSite, []string) {
	t.Helper()
	site := &fakeSite{}
	srv := httptest.NewServer(site)
	t.Cleanup(srv.Close)
	env, _ := siteEnv(t, srv.URL)
	return site, env
}

// A connection that drops while login waits for the approval costs one check, not the login: before
// the reply, in the middle of it, at a gateway in front of v2.video, or at an overloaded Convex, whose reason is shown.
func TestLoginWaitsThroughADroppedConnection(t *testing.T) {
	for _, fault := range []string{"drop", "cut", "502", "503"} {
		t.Run(fault, func(t *testing.T) {
			site, env := newSite(t)
			site.pollFaults = []string{fault}
			out, code := v2(t, t.TempDir(), env, "login")
			if code != 0 || !strings.Contains(out, "Signed in as Alice") {
				t.Fatalf("exit %d: %s", code, out)
			}
			if fault == "503" && !strings.Contains(out, "cli:whoami got HTTP 503: Service temporarily unavailable") {
				t.Fatalf("Convex's reason was lost: %s", out)
			}
			if out, code := v2(t, t.TempDir(), env, "logout"); code != 0 || !site.loggedOut {
				t.Fatalf("the login wasn't kept: exit %d: %s", code, out)
			}
		})
	}
}

// An agent runs v2 login before every push. Signed in already, it must not print a new code and wait for an approval.
func TestLoginWhenSignedInSaysSo(t *testing.T) {
	site, env := cloneSite(t)
	starts := slices.Index(site.calls, "cli:startLogin")
	out, code := v2(t, t.TempDir(), env, "login")
	if code != 0 || !strings.Contains(out, "Already signed in as Alice (a@example.com)") || slices.Index(site.calls[starts+1:], "cli:startLogin") != -1 {
		t.Fatalf("exit %d: %s", code, out)
	}
	site.revoked = true
	site.calls = nil
	if out, code := v2(t, t.TempDir(), env, "login"); !slices.Contains(site.calls, "cli:startLogin") {
		t.Fatalf("a token the site no longer knows didn't start a new login: exit %d: %s", code, out)
	}
	other, otherEnv := newSite(t)
	home := slices.IndexFunc(env, func(e string) bool { return strings.HasPrefix(e, "HOME=") })
	otherEnv = append(otherEnv, env[home]) // the same computer, signed in to the first site
	if out, code := v2(t, t.TempDir(), otherEnv, "login"); code != 0 || !slices.Contains(other.calls, "cli:startLogin") {
		t.Fatalf("a login for another server was taken as this one: exit %d: %s", code, out)
	}
}

// Only a call that got no answer is worth another try, and only once there is an approval to lose.
func TestLoginStopsAtOnce(t *testing.T) {
	site, env := newSite(t)
	site.pollError = "no such function"
	if out, code := v2(t, t.TempDir(), env, "login"); code != 1 || !strings.Contains(out, "no such function") {
		t.Fatalf("the site's refusal: exit %d: %s", code, out)
	}
	out, code := v2(t, t.TempDir(), append(env, "V2_SERVER=http://127.0.0.1:1"), "login")
	if code != 1 || !strings.Contains(out, "can't reach v2.video") {
		t.Fatalf("offline: exit %d: %s", code, out)
	}
}

func TestSaveNeedsAPrompt(t *testing.T) {
	proj := t.TempDir()
	newProject(t, proj)
	if out, code := v2(t, proj, gitIdentity, "save", "-m", "x", "--change", "y"); code != 1 || !strings.Contains(out, "--prompt") {
		t.Fatalf("a save with no prompt: exit %d: %s", code, out)
	}
	if tags := git(t, proj, "tag", "--list"); tags != "" {
		t.Fatalf("a refused save made a version: %s", tags)
	}
}

func TestSaveRefusesSecrets(t *testing.T) {
	proj := t.TempDir()
	newProject(t, proj)
	for _, args := range [][]string{
		{"--prompt", "use my key sk-proj-AbCdEf0123456789xYz_9876 for the voice", "-m", "x", "--change", "y"},
		{"--prompt", "make it", "-m", "uses ELEVENLABS_API_KEY=el_abcdef123456", "--change", "y"},
		{"--prompt", "make it", "-m", "x", "--change", "0:01 voice token: ghp_0123456789abcdefghijABCDEFGHIJ0123456789"},
	} {
		out, code := v2(t, proj, gitIdentity, append([]string{"save"}, args...)...)
		if code != 1 || !strings.Contains(out, "[REDACTED]") {
			t.Fatalf("%v: want a refusal naming [REDACTED], exit %d: %s", args, code, out)
		}
	}
	if tags := git(t, proj, "tag", "--list"); tags != "" {
		t.Fatalf("a refused save made a version: %s", tags)
	}
	save(t, proj, "-m", "x", "--prompt", "make the password screen tighter and add a token counter", "--change", "y")
}

// An agent in a new session knows the project has a link only from what save prints, and treated
// "run `v2 push` to update its link" as something to offer the user: half the sessions never pushed.
func TestSaveTellsTheAgentToPushOnceTheProjectHasALink(t *testing.T) {
	_, env := cloneSite(t)
	proj := t.TempDir()
	newProject(t, proj)
	put(t, proj, "src/edit.txt", "a")
	if out := save(t, proj, "-m", "one", "--change", "one"); strings.Contains(out, "v2 push") {
		t.Fatalf("nothing is pushed yet, but save said: %s", out)
	}
	if out, code := v2(t, proj, env, "push"); code != 0 {
		t.Fatalf("push: exit %d: %s", code, out)
	}
	put(t, proj, "src/edit.txt", "b")
	out := save(t, proj, "-m", "two", "--change", "two")
	if !strings.Contains(out, "run `v2 push` now") || !strings.Contains(out, "don't ask first") {
		t.Fatalf("save on a project with a link should tell the agent to push without asking: %s", out)
	}
}

func TestPublishAndUnpublish(t *testing.T) {
	site := &fakeSite{uploads: map[string][]byte{}, blobs: map[string]bool{}}
	srv := httptest.NewServer(site)
	defer srv.Close()
	site.url = srv.URL
	env, _ := siteEnv(t, srv.URL)

	proj := t.TempDir()
	newProject(t, proj)
	put(t, proj, "src/edit.txt", "a")
	save(t, proj, "-m", "first", "--change", "title card")
	if out, code := v2(t, t.TempDir(), env, "publish"); code != 1 || strings.Contains(out, "Run v2 push first") {
		t.Fatalf("publish outside a project must say so, not ask for a push: exit %d: %s", code, out)
	}
	if out, code := v2(t, proj, env, "publish"); code != 1 || !strings.Contains(out, "Run v2 push first") {
		t.Fatalf("publish before any push: exit %d: %s", code, out)
	}
	if out, code := v2(t, proj, env, "login"); code != 0 {
		t.Fatalf("login: %s", out)
	}
	if out, code := v2(t, proj, env, "push"); code != 0 {
		t.Fatalf("push: %s", out)
	}
	link := "https://site.test/v/demo"
	for _, step := range []struct{ cmd, want, state string }{
		{"publish", "public: anyone with the link can watch it: " + link, "public"},
		{"publish", "already public: " + link, "public"},
		{"unpublish", "private: only you can watch it now", "private"},
		{"unpublish", "already private: " + link, "private"},
	} {
		out, code := v2(t, proj, env, step.cmd)
		site.mu.Lock()
		state := site.visibility["vid1"]
		site.mu.Unlock()
		if code != 0 || !strings.Contains(out, step.want) || state != step.state {
			t.Fatalf("%s: exit %d, site has %q: %s", step.cmd, code, state, out)
		}
	}
}
