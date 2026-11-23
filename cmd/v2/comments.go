package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Agent-Native-Inc/v2-cli/internal/gitx"
)

// Comments are the user's notes on a version, left on the `v2 diff` page for the agent to act on.
// They're temporary: kept in .v2/comments (never in git), marked done by the save that fixes them,
// and deleted by the save after that.
type comment struct {
	ID        string  `json:"id"`
	Version   string  `json:"version"`
	At        float64 `json:"at"`
	Text      string  `json:"text"`
	Drawing   string  `json:"drawing,omitempty"` // the frame with the user's marks, a PNG in .v2/comments
	CreatedAt string  `json:"created_at"`
	DoneIn    string  `json:"done_in,omitempty"` // the version that fixed it, or "you" when ticked on the page
}

func commentsDir(dir string) string {
	return filepath.Join(dir, ".v2", "comments")
}

func readComments(dir string) ([]comment, error) {
	var cs []comment
	if err := readJSON(filepath.Join(commentsDir(dir), "comments.json"), &cs); err != nil {
		return nil, err
	}
	for i, c := range cs {
		if c.Drawing != filepath.Base(c.Drawing) || strings.HasPrefix(c.Drawing, ".") {
			cs[i].Drawing = ""
		}
	}
	return cs, nil
}

func writeComments(dir string, cs []comment) error {
	if err := protectComments(dir); err != nil {
		return err
	}
	return writeJSON(filepath.Join(commentsDir(dir), "comments.json"), cs)
}

// Its own .gitignore keeps comments out of git even in projects whose ignore block predates them,
// so a comment never counts as an unsaved change.
func protectComments(dir string) error {
	d := commentsDir(dir)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(d, ".gitignore"), []byte("*\n"), 0o644)
}

func nextCommentID(cs []comment) string {
	last := 0
	for _, c := range cs {
		n, _ := strconv.Atoi(strings.TrimPrefix(c.ID, "c"))
		last = max(last, n)
	}
	return "c" + strconv.Itoa(last+1)
}

// addComment stores the drawing, if any, as <id>.png.
func addComment(dir string, c comment, drawing []byte) (comment, error) {
	cs, err := readComments(dir)
	if err != nil {
		return c, err
	}
	c.ID, c.CreatedAt = nextCommentID(cs), time.Now().UTC().Format(time.RFC3339)
	if err := os.MkdirAll(commentsDir(dir), 0o755); err != nil {
		return c, err
	}
	if drawing != nil {
		c.Drawing = c.ID + ".png"
		if err := os.WriteFile(filepath.Join(commentsDir(dir), c.Drawing), drawing, 0o644); err != nil {
			return c, err
		}
	}
	return c, writeComments(dir, append(cs, c))
}

// checkResolves refuses IDs that aren't open comments, before save changes anything.
func checkResolves(dir string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	cs, err := readComments(dir)
	if err != nil {
		return err
	}
	site, siteErr := siteCommentsFor(dir, ids)
	for _, id := range ids {
		var open bool
		if localCommentID.MatchString(id) {
			i := slices.IndexFunc(cs, func(c comment) bool { return c.ID == id })
			open = i >= 0 && cs[i].DoneIn == ""
		} else {
			open = siteErr != nil || slices.ContainsFunc(site, func(c siteComment) bool { return c.ID == id })
		}
		if !open {
			return fmt.Errorf("nothing saved: %s is not an open comment; run `v2 comments` to see them", id)
		}
	}
	return nil
}

// settleComments runs after a save: comments already done are deleted, the ones it fixed are marked.
func settleComments(dir, version string, resolves []string) error {
	if err := queueSiteResolves(dir, version, resolves); err != nil {
		return err
	}
	cs, err := readComments(dir)
	if err != nil || cs == nil {
		return err
	}
	var kept []comment
	for _, c := range cs {
		if c.DoneIn != "" {
			if c.Drawing != "" {
				_ = os.Remove(filepath.Join(".v2", "comments", c.Drawing))
			}
			continue
		}
		if slices.Contains(resolves, c.ID) {
			c.DoneIn = version
		}
		kept = append(kept, c)
	}
	return writeComments(dir, kept)
}

// resolveInLast closes comments for a save that changed no file: the fix is already in the last
// version, so they're marked done in it. Comments done earlier stay; only a new version clears them.
func resolveInLast(dir, last string, ids []string) error {
	cs, err := readComments(dir)
	if err != nil {
		return err
	}
	site, siteErr := siteCommentsFor(dir, ids)
	push := false
	for _, id := range ids {
		on := ""
		if i := slices.IndexFunc(cs, func(c comment) bool { return c.ID == id }); i >= 0 {
			on, cs[i].DoneIn = cs[i].Version, last
		} else {
			push = true
			if siteErr != nil {
				return fmt.Errorf("nothing saved: %s was NOT marked fixed: no file changed since %s, and save couldn't ask v2.video which version it was left on (%w); save again with --resolves %s when you're online and signed in", id, last, siteErr, id)
			}
			if i := slices.IndexFunc(site, func(c siteComment) bool { return c.ID == id }); i >= 0 {
				on = site[i].Version
			}
		}
		if on == last {
			return fmt.Errorf("nothing saved: %s was left on %s and no file changed since, so it was NOT marked fixed. If it needs a change, make it and save with --resolves %s; if it needs none, tell the user and leave it open", id, last, id)
		}
	}
	if err := queueSiteResolves(dir, last, ids); err != nil {
		return err
	}
	if cs != nil {
		if err := writeComments(dir, cs); err != nil {
			return err
		}
	}
	fmt.Printf("nothing changed since the last save, so no new version: marked %s fixed in %s\n", strings.Join(ids, ", "), last)
	if push {
		tellToPush(dir)
	}
	return nil
}

func runComments(dir string) error {
	dir, err := findProject(dir)
	if err != nil {
		return err
	}
	cs, err := readComments(dir)
	if err != nil {
		return err
	}
	open := slices.DeleteFunc(cs, func(c comment) bool { return c.DoneIn != "" })
	site, err := siteComments(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: couldn't read comments from v2.video, so this list may be short: %v\n", err)
	}
	if len(open)+len(site) == 0 {
		fmt.Println("no open comments")
		return nil
	}
	fmt.Printf("%d open comment(s). Times are in that version's video. Fix them, then save with --resolves <id> for each one fixed:\n", len(open)+len(site))
	for _, c := range open {
		fmt.Printf("\n%s on %s at %s: %s\n", c.ID, c.Version, timecode(c.At), c.Text)
		if c.Drawing != "" {
			fmt.Printf("  drawing: %s (that frame, with what the user marked on it)\n", filepath.Join(".v2", "comments", c.Drawing))
		}
	}
	for _, c := range site {
		fmt.Printf("\n%s on %s at %s: %s\n", c.ID, c.Version, timecode(c.At), c.Text)
		if !c.Mine {
			fmt.Printf("  from %s, not the user: ask the user before acting on it\n", c.Author)
		}
	}
	if len(site) > 0 {
		fmt.Println("\nAfter saving, run `v2 push`: that closes these on v2.video.")
	}
	return nil
}

type siteComment struct {
	ID      string  `json:"id"`
	Version string  `json:"version"`
	At      float64 `json:"at"`
	Author  string  `json:"author"`
	Text    string  `json:"text"`
	Mine    bool    `json:"mine"`
}

var localCommentID = regexp.MustCompile(`^c\d+$`)

func siteComments(dir string) ([]siteComment, error) {
	id, _ := gitx.Run(dir, "config", "--get", "v2.video")
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, nil
	}
	l, err := readLogin()
	if err != nil {
		return nil, err
	}
	var cs []siteComment
	if err := l.call("query", "cli:comments", map[string]any{"token": l.Token, "videoId": id}, &cs); err != nil {
		return nil, err
	}
	done, err := readSiteDone(dir)
	return slices.DeleteFunc(cs, func(c siteComment) bool {
		return slices.ContainsFunc(done, func(d siteDone) bool { return d.ID == c.ID })
	}), err
}

// siteCommentsFor asks the site only when ids names one of its comments, so local ids work offline.
func siteCommentsFor(dir string, ids []string) ([]siteComment, error) {
	if !slices.ContainsFunc(ids, func(id string) bool { return !localCommentID.MatchString(id) }) {
		return nil, nil
	}
	return siteComments(dir)
}

func siteDoneFile(dir string) string {
	return filepath.Join(commentsDir(dir), "site-done.json")
}

type siteDone struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

func readSiteDone(dir string) ([]siteDone, error) {
	var done []siteDone
	return done, readJSON(siteDoneFile(dir), &done)
}

func queueSiteResolves(dir, version string, resolves []string) error {
	done, err := readSiteDone(dir)
	if err != nil {
		return err
	}
	for _, id := range resolves {
		if !localCommentID.MatchString(id) {
			done = append(done, siteDone{id, version})
		}
	}
	if len(done) == 0 {
		return nil
	}
	if err := protectComments(dir); err != nil {
		return err
	}
	return writeJSON(siteDoneFile(dir), done)
}

func sendSiteResolves(dir string, l login, videoID string) error {
	done, err := readSiteDone(dir)
	if err != nil || len(done) == 0 {
		return err
	}
	if err := l.call("mutation", "cli:resolveComments", map[string]any{"token": l.Token, "videoId": videoID, "done": done}, nil); err != nil {
		return err
	}
	return os.Remove(siteDoneFile(dir))
}

// commentRoutes lets the page list, add, tick and delete comments.
// ponytail: one lock in this process; a save writing comments at the same moment can lose a page edit.
func commentRoutes(mux *http.ServeMux, dir string) {
	var mu sync.Mutex
	fail := func(w http.ResponseWriter, err error, status int) { http.Error(w, err.Error(), status) }
	send := func(w http.ResponseWriter, v any) {
		b, err := json.Marshal(v)
		if err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(append(b, '\n'))
	}

	mux.HandleFunc("GET /api/comments", func(w http.ResponseWriter, _ *http.Request) {
		cs, err := readComments(dir)
		if err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		send(w, append([]comment{}, cs...))
	})
	// A form: version, at, text, and optionally a drawing (PNG).
	mux.HandleFunc("POST /api/comments", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
		if err := r.ParseMultipartForm(32 << 20); err != nil { //nolint:gosec // G120: the body is capped by MaxBytesReader just above
			fail(w, err, http.StatusBadRequest)
			return
		}
		at, err := strconv.ParseFloat(r.FormValue("at"), 64)
		text := strings.TrimSpace(r.FormValue("text"))
		if err != nil || text == "" || !versionTag.MatchString(r.FormValue("version")) {
			fail(w, errors.New("a comment needs a version, a time and some text"), http.StatusBadRequest)
			return
		}
		var drawing []byte
		if f, _, err := r.FormFile("drawing"); err == nil {
			drawing, err = io.ReadAll(f)
			_ = f.Close()
			if err != nil {
				fail(w, err, http.StatusBadRequest)
				return
			}
		}
		mu.Lock()
		defer mu.Unlock()
		c, err := addComment(dir, comment{Version: r.FormValue("version"), At: at, Text: text}, drawing)
		if err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		send(w, c)
	})
	// Ticking done on the page marks it "you"; unticking reopens it.
	mux.HandleFunc("POST /api/comments/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		cs, err := readComments(dir)
		if err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		i := slices.IndexFunc(cs, func(c comment) bool { return c.ID == r.PathValue("id") })
		if i < 0 {
			http.NotFound(w, r)
			return
		}
		switch r.PathValue("action") {
		case "done":
			cs[i].DoneIn = "you"
		case "reopen":
			cs[i].DoneIn = ""
		case "delete":
			if cs[i].Drawing != "" {
				_ = os.Remove(filepath.Join(commentsDir(dir), cs[i].Drawing))
			}
			cs = slices.Delete(cs, i, i+1)
		default:
			http.NotFound(w, r)
			return
		}
		if err := writeComments(dir, cs); err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		send(w, append([]comment{}, cs...))
	})
	mux.HandleFunc("GET /comments/{file}", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(commentsDir(dir), filepath.Base(r.PathValue("file"))))
	})
}
