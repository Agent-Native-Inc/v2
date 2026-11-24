package main

// Clone's git side: the versions pushed from the CLI come back from their bundles, checked before use.

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Agent-Native-Inc/v2/internal/gitx"
	"github.com/Agent-Native-Inc/v2/internal/store"
	"github.com/Agent-Native-Inc/v2/internal/walk"
)

func (c *cloner) rebuild(plan clonePlan) error {
	if _, err := gitx.Run(c.dir, "init", "-q"); err != nil {
		return err
	}
	if len(plan.git) == 0 {
		if err := setUpProject(c.dir); err != nil {
			return err
		}
		if err := c.fetchReplayed(plan, nil); err != nil {
			return err
		}
		return c.replay(plan, "")
	}

	tip := plan.git[len(plan.git)-1].tag()
	if err := c.fetchHistory(plan.git); err != nil {
		return err
	}
	media, err := c.gitMedia(plan.git)
	if err != nil {
		return err
	}
	if err := c.fetchReplayed(plan, media); err != nil {
		return err
	}
	// The second checkout undoes what setup refreshes in tracked files (AGENTS.md, .gitignore), which a
	// version from an older v2 would otherwise show as unsaved changes.
	if err := checkout(c.dir, tip); err != nil {
		return err
	}
	if err := setUpProject(c.dir); err != nil {
		return err
	}
	if err := checkout(c.dir, tip); err != nil {
		return err
	}
	assets, err := assetsAt(c.dir, tip)
	if err != nil {
		return err
	}
	for _, p := range slices.Sorted(maps.Keys(assets)) {
		if !writable(c.dir, p) {
			return fmt.Errorf("%s: %s can't be written inside the project; refusing to clone it", tip, p)
		}
		if err := c.place(p, assets[p].Sha256); err != nil {
			return err
		}
	}
	info, err := commitAt(c.dir, tip)
	if err != nil {
		return err
	}
	return c.replay(plan, info.Render)
}

// checkout is reset --hard, except for the Mac's ._ files an old version tracks: they stay in git
// marked skip-worktree, so git never writes them, and ignores the Mac's own on an exFAT drive.
func checkout(dir, tip string) error {
	if _, err := gitx.Run(dir, "reset", "-q", tip); err != nil {
		return err
	}
	tracked, err := gitx.Run(dir, "ls-files", "-z")
	if err != nil {
		return err
	}
	mac := slices.DeleteFunc(gitx.SplitZ(tracked), func(p string) bool { return !walk.AppleDouble(p) })
	if len(mac) > 0 {
		if _, err := gitx.Run(dir, append([]string{"update-index", "--skip-worktree", "--"}, mac...)...); err != nil {
			return err
		}
	}
	if _, err := gitx.Run(dir, "checkout-index", "-a", "-f"); err != nil {
		return err
	}
	// Git before 2.37 (Ubuntu 22.04 has 2.34) writes skip-worktree files too.
	for _, p := range mac {
		if err := os.Remove(filepath.Join(dir, p)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// fetchHistory brings each git version's commits in from its bundle, and checks what they hold.
func (c *cloner) fetchHistory(versions []siteVersion) error {
	needs := make([]need, 0, len(versions))
	for _, v := range versions {
		needs = append(needs, need{sum: v.Bundle, path: fmt.Sprintf("V%d's history", v.N), must: true})
	}
	if err := c.fetch("history", needs); err != nil {
		return err
	}
	for _, v := range versions {
		tag := v.tag()
		ref := "refs/tags/" + tag
		if _, err := gitx.Run(c.dir, "fetch", "-q", store.Path(c.objects, v.Bundle), ref+":"+ref); err != nil {
			return fmt.Errorf("V%d: %w", v.N, err)
		}
		got, err := gitx.Run(c.dir, "rev-parse", tag+"^{commit}")
		if err != nil {
			return err
		}
		if strings.TrimSpace(got) != v.Commit {
			return fmt.Errorf("V%d's history is commit %s, not %s as the site says", v.N, strings.TrimSpace(got), v.Commit)
		}
		if err := c.checkTree(tag); err != nil {
			return err
		}
	}
	return nil
}

// gitMedia lists the media of every git version, so restore and diff work on all of them.
func (c *cloner) gitMedia(versions []siteVersion) ([]need, error) {
	var needs []need
	for _, v := range versions {
		tag := v.tag()
		assets, err := savedAssetsAt(c.dir, tag)
		if err != nil {
			return nil, err
		}
		for _, p := range slices.Sorted(maps.Keys(assets)) {
			if !insideProject(p) {
				return nil, fmt.Errorf("%s lists %s, which isn't a file inside the project; refusing to clone it", tag, p)
			}
			needs = append(needs, need{sum: assets[p].Sha256, path: tag + " " + p, must: true})
		}
	}
	return needs, nil
}

// v2Records are the only files a version may track in .v2. Anything else there, like a comments
// file, would be read as the user's own.
var v2Records = []string{assetsFile, commitFile, configFile, ".v2/ignore"}

// checkTree refuses a version that tracks other files in .v2, or whose symlinks leave the project or
// reach into .git or .v2. A clean relative target can't climb back out through another link.
func (c *cloner) checkTree(tag string) error {
	entries, err := lsTree(c.dir, tag)
	if err != nil {
		return err
	}
	for _, e := range entries {
		// On exFAT the Mac writes ._name next to each of v2's records, and v0.9.2 saved them.
		top, rest, _ := strings.Cut(e.path, "/")
		macFile := walk.AppleDouble(e.path) && !strings.Contains(rest, "/")
		if strings.EqualFold(top, ".v2") && !slices.Contains(v2Records, e.path) && !macFile {
			return fmt.Errorf("%s: %s is inside .v2, where only v2's own records belong; refusing to clone it", tag, e.path)
		}
		if e.mode != "120000" {
			continue
		}
		target, err := gitx.Run(c.dir, "cat-file", "blob", e.object)
		if err != nil {
			return err
		}
		inside := filepath.Join(path.Dir(e.path), target)
		if path.IsAbs(target) || path.Clean(target) != target || inside == ".." || strings.HasPrefix(inside, "../") {
			return fmt.Errorf("%s: the symlink %s points outside the project; refusing to clone it", tag, e.path)
		}
		if top, _, _ := strings.Cut(inside, "/"); strings.EqualFold(top, ".git") || strings.EqualFold(top, ".v2") {
			return fmt.Errorf("%s: the symlink %s points into .git or .v2; refusing to clone it", tag, e.path)
		}
	}
	return nil
}
