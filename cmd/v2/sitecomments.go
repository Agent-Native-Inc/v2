package main

// Comments left on v2.video: read from the site, and the ones a save fixed queued until push closes them there.

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"

	"github.com/agentnativeinc/v2/internal/gitx"
)

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
	id := gitx.Config(dir, "v2.video")
	if id == "" {
		return nil, nil
	}
	l, err := readLogin()
	if err != nil {
		return nil, err
	}
	var cs []siteComment
	if err := l.call("query", "cli:comments", l.authed(map[string]any{"videoId": id}), &cs); err != nil {
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
	if err := l.call("mutation", "cli:resolveComments", l.authed(map[string]any{"videoId": videoID, "done": done}), nil); err != nil {
		return err
	}
	return os.Remove(siteDoneFile(dir))
}
