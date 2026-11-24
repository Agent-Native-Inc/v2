package main

// The versions page's comment routes: list, add, tick and delete comments.
// One lock in this process; a save writing comments at the same moment can lose a page edit.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

func fail(w http.ResponseWriter, err error, status int) {
	http.Error(w, err.Error(), status)
}

func sendJSON(w http.ResponseWriter, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(append(b, '\n'))
}

func (s *diffServer) listComments(w http.ResponseWriter, _ *http.Request) {
	cs, err := readComments(s.dir)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	sendJSON(w, append([]comment{}, cs...))
}

// addComment takes a form: version, at, text, and optionally a drawing (PNG).
func (s *diffServer) addComment(w http.ResponseWriter, r *http.Request) {
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
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := addComment(s.dir, comment{Version: r.FormValue("version"), At: at, Text: text}, drawing)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	sendJSON(w, c)
}

// editComment marks a comment done, reopens it or deletes it. Ticking done on the page marks it
// "you"; unticking reopens it.
func (s *diffServer) editComment(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cs, err := readComments(s.dir)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	i := commentIndex(cs, r.PathValue("id"))
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
		removeDrawing(s.dir, cs[i])
		cs = slices.Delete(cs, i, i+1)
	default:
		http.NotFound(w, r)
		return
	}
	if err := writeComments(s.dir, cs); err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	sendJSON(w, append([]comment{}, cs...))
}

func (s *diffServer) drawing(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, filepath.Join(commentsDir(s.dir), filepath.Base(r.PathValue("file"))))
}
