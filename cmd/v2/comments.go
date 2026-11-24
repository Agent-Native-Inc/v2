package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Agent-Native-Inc/v2/internal/walk"
)

// Comments are the user's comments on a version, left on the `v2 diff` page for the agent to act on.
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

// commentIndex is the position of comment id in cs, or -1.
func commentIndex(cs []comment, id string) int {
	return slices.IndexFunc(cs, func(c comment) bool { return c.ID == id })
}

// removeDrawing deletes c's drawing, if it has one.
func removeDrawing(dir string, c comment) {
	if c.Drawing != "" {
		_ = os.Remove(filepath.Join(commentsDir(dir), c.Drawing))
	}
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
	return walk.WriteFile(filepath.Join(d, ".gitignore"), []byte("*\n"))
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
	if drawing != nil {
		if err := protectComments(dir); err != nil { // the folder, before the drawing goes in it
			return c, err
		}
		c.Drawing = c.ID + ".png"
		if err := walk.WriteFile(filepath.Join(commentsDir(dir), c.Drawing), drawing); err != nil {
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
			i := commentIndex(cs, id)
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
			removeDrawing(dir, c)
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
		if i := commentIndex(cs, id); i >= 0 {
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
			fmt.Printf("  drawing: %s (that frame, with what the user marked on it)\n", filepath.Join(commentsDir(dir), c.Drawing))
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
