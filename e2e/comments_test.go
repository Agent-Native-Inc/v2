package e2e

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The loop Alice asked for: comments left on the page reach the agent, and the save that fixes them closes them.
func TestCommentsReachTheAgentAndCloseOnSave(t *testing.T) {
	proj := t.TempDir()
	newProject(t, proj)
	put(t, proj, "src/edit.txt", "a")
	save(t, proj, "-m", "first", "--change", "title card")

	url := serveDiff(t, proj)

	post := func(fields map[string]string, drawing []byte) {
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
		resp, err := http.Post(url+"api/comments", form.FormDataContentType(), &body)
		if err != nil {
			t.Fatalf("post comment: %v", err)
		}
		must(t, resp.Body.Close())
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("post comment: %v", resp.Status)
		}
	}
	post(map[string]string{"version": "v1", "at": "13.2", "text": "make this card bigger"}, []byte("\x89PNG fake"))
	post(map[string]string{"version": "v1", "at": "24", "text": "hold the branch a beat longer"}, nil)

	if got := read(t, filepath.Join(proj, ".v2/comments/c1.png")); got != "\x89PNG fake" {
		t.Fatalf("drawing = %q", got)
	}
	// Comments are temporary: git never sees them, so they don't block a restore.
	if status := git(t, proj, "status", "--porcelain"); status != "" {
		t.Fatalf("comments show up in git: %q", status)
	}

	out, _ := v2(t, proj, nil, "comments")
	for _, want := range []string{"c1 on v1 at 0:13.2: make this card bigger", "drawing: .v2/comments/c1.png", "c2 on v1 at 0:24: hold the branch"} {
		if !strings.Contains(out, want) {
			t.Fatalf("comments output lacks %q:\n%s", want, out)
		}
	}

	put(t, proj, "src/edit.txt", "b")
	if out, code := v2(t, proj, gitIdentity, "save", "--prompt", "make it", "-m", "x", "--change", "y", "--resolves", "c9"); code != 1 || !strings.Contains(out, "c9 is not an open comment") {
		t.Fatalf("exit %d: %s", code, out)
	}
	if tags := git(t, proj, "tag", "--list"); tags != "v1\n" {
		t.Fatalf("a refused save made a version: %q", tags)
	}

	save(t, proj, "-m", "bigger card", "--change", "0:12-0:15 card is bigger", "--resolves", "c1")
	if out, _ := v2(t, proj, nil, "comments"); strings.Contains(out, "c1") || !strings.Contains(out, "c2") {
		t.Fatalf("after resolving c1:\n%s", out)
	}
	if !strings.Contains(read(t, filepath.Join(proj, ".v2/comments/comments.json")), `"done_in": "v2"`) {
		t.Fatal("c1 not marked done in v2")
	}

	// The save after that clears it, drawing included.
	put(t, proj, "src/edit.txt", "c")
	save(t, proj, "-m", "hold", "--change", "0:22-0:25 branch holds longer", "--resolves", "c2")
	if _, err := os.Stat(filepath.Join(proj, ".v2/comments/c1.png")); !os.IsNotExist(err) {
		t.Fatal("c1's drawing outlived the next save")
	}
	if strings.Contains(read(t, filepath.Join(proj, ".v2/comments/comments.json")), `"c1"`) {
		t.Fatal("c1 outlived the next save")
	}

	// A save that changes no file makes no version: it closes the comment in the last one, and
	// c2, done in that same version, stays on the page.
	post(map[string]string{"version": "v2", "at": "3", "text": "one more beat"}, nil)
	out, code := v2(t, proj, gitIdentity, "save", "--prompt", "make it", "-m", "hold", "--change", "0:22-0:25 branch holds longer", "--resolves", "c3")
	if code != 0 || !strings.Contains(out, "c3") || !strings.Contains(out, "fixed in v3") || strings.Contains(out, "v2 push") {
		t.Fatalf("a save with only --resolves: exit %d: %s", code, out)
	}
	if tags := git(t, proj, "tag", "--list"); tags != "v1\nv2\nv3\n" {
		t.Fatalf("a save that changed no file made a version: %q", tags)
	}
	if left := read(t, filepath.Join(proj, ".v2/comments/comments.json")); strings.Count(left, `"done_in": "v3"`) != 2 {
		t.Fatalf("c2 and c3 should both be done in v3:\n%s", left)
	}
	post(map[string]string{"version": "v3", "at": "1", "text": "too long now"}, nil)
	if out, code := v2(t, proj, gitIdentity, "save", "--prompt", "make it", "-m", "x", "--change", "y", "--resolves", "c4"); code != 1 || !strings.Contains(out, "c4 was left on v3") {
		t.Fatalf("a comment on the last version, nothing changed: exit %d: %s", code, out)
	}
	if out, _ := v2(t, proj, nil, "comments"); !strings.Contains(out, "c4 on v3") {
		t.Fatalf("c4 should still be open:\n%s", out)
	}
}

// Another website open in the same browser must not reach the page's server.
func TestDiffRefusesOtherSites(t *testing.T) {
	proj := t.TempDir()
	newProject(t, proj)
	put(t, proj, "src/edit.txt", "a")
	save(t, proj, "-m", "first", "--change", "title card")
	url := serveDiff(t, proj)
	self := strings.TrimSuffix(url, "/")

	send := func(method, path, host string, headers map[string]string) int {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		must(t, form.WriteField("version", "v1"))
		must(t, form.WriteField("at", "1"))
		must(t, form.WriteField("text", "from "+headers["Origin"]))
		must(t, form.Close())
		req, err := http.NewRequest(method, url+path, &body)
		must(t, err)
		req.Header.Set("Content-Type", form.FormDataContentType())
		if host != "" {
			req.Host = host
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		must(t, res.Body.Close())
		return res.StatusCode
	}
	if code := send("POST", "api/comments", "", map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}); code != 403 {
		t.Fatalf("cross-site comment: %d, want 403", code)
	}
	if code := send("POST", "api/comments", "", map[string]string{"Sec-Fetch-Site": "same-site"}); code != 403 {
		t.Fatalf("same-site comment without an Origin: %d, want 403", code)
	}
	if code := send("GET", "api/versions", "evil.example:"+self[strings.LastIndex(self, ":")+1:], nil); code != 403 {
		t.Fatalf("rebound host: %d, want 403", code)
	}
	if code := send("POST", "api/comments", "", map[string]string{"Origin": self, "Sec-Fetch-Site": "same-origin"}); code != 200 {
		t.Fatalf("the page's own comment: %d, want 200", code)
	}
	if code := send("POST", "api/comments", "", map[string]string{"Origin": "null"}); code != 403 {
		t.Fatalf("comment from a sandboxed page: %d, want 403", code)
	}
	if code := send("POST", "api/comments", "", map[string]string{"Origin": "http://localhost:5173", "Sec-Fetch-Site": "same-origin"}); code != 200 {
		t.Fatalf("a comment through the page's dev server: %d, want 200", code)
	}
	if code := send("GET", "api/versions", "localhost"+self[strings.LastIndex(self, ":"):], nil); code != 200 {
		t.Fatalf("localhost: %d, want 200", code)
	}
	if out, _ := v2(t, proj, nil, "comments"); strings.Contains(out, "evil") || strings.Count(out, "from http") != 2 {
		t.Fatalf("comments:\n%s", out)
	}
}

// A comment's drawing is a file name in .v2/comments; a path climbing out of it must never reach
// the agent or be deleted when the comment is settled.
func TestDrawingPathStaysInComments(t *testing.T) {
	proj := t.TempDir()
	newProject(t, proj)
	put(t, proj, "src/a.txt", "a")
	save(t, proj, "-m", "one", "--change", "one")
	outside := filepath.Join(t.TempDir(), "precious.txt")
	must(t, os.WriteFile(outside, []byte("keep"), 0o644))
	climb := strings.Repeat("../", 40) + strings.TrimPrefix(outside, "/")
	put(t, proj, ".v2/comments/comments.json", `[{"id":"c1","version":"v1","at":1,"text":"open","drawing":"`+climb+`","created_at":"x"},`+
		`{"id":"c2","version":"v1","at":1,"text":"done","drawing":"`+climb+`","created_at":"x","done_in":"v1"}]`)

	if out, _ := v2(t, proj, nil, "comments"); strings.Contains(out, "precious") {
		t.Fatalf("comments pointed the agent outside the project:\n%s", out)
	}
	put(t, proj, "src/a.txt", "b")
	save(t, proj, "-m", "two", "--change", "two")
	if !exists(outside) {
		t.Fatal("save deleted a file outside the project")
	}
}

func TestCommentsSayWhenSiteCommentsNeedALogin(t *testing.T) {
	proj := t.TempDir()
	newProject(t, proj)
	git(t, proj, "config", "v2.video", "vid1")
	if out, _ := v2(t, proj, []string{"HOME=" + t.TempDir()}, "comments"); !strings.Contains(out, "v2 login") {
		t.Fatalf("site comments were hidden without saying so:\n%s", out)
	}
}
