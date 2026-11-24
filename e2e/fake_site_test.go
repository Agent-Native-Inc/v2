package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

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
	srv       *httptest.Server
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

// startSite starts a fake site that lives until the test ends.
func startSite(t *testing.T) *fakeSite {
	t.Helper()
	site := &fakeSite{
		uploads: map[string][]byte{}, types: map[string]string{}, blobs: map[string]bool{},
		parts: map[string]map[int][]byte{}, starts: map[string]int{}, etagsOf: map[string][]string{},
		stored: map[string][]byte{}, r2: map[string]bool{}, downloads: map[string]int{}, visibility: map[string]string{},
	}
	site.srv = httptest.NewServer(site)
	t.Cleanup(site.srv.Close)
	site.url = site.srv.URL
	return site
}

// newSite is a fake site and the env to reach it, with nobody signed in.
func newSite(t *testing.T) (*fakeSite, []string) {
	t.Helper()
	site := startSite(t)
	env, _ := siteEnv(t, site.url)
	return site, env
}

// cloneSite is a fake site and the env to reach it, signed in.
func cloneSite(t *testing.T) (*fakeSite, []string) {
	t.Helper()
	site, env := newSite(t)
	mustRun(t, t.TempDir(), env, "login")
	return site, env
}

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

// serve registers bytes as a blob on the fake site and returns their hash.
func (f *fakeSite) serve(b []byte) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stored[sum(b)] = b
	return sum(b)
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
	return fmt.Sprintf("%q", sum(part)[:32])
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
	if got := sum(all); got != sha || float64(len(all)) != args["size"] {
		refuse(w, "bytes don't match "+got)
		return
	}
	delete(f.parts, id)
	f.blobs[sha] = true
	f.uploads[sha] = all
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
		f.downloads[blob]++
	}
	_, _ = w.Write(cut(data))
}
