package main

// Replaying old uploads, which have no git: each one's files go in the folder and a normal save makes its tag.

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/agentnativeinc/v2/internal/store"
	"github.com/agentnativeinc/v2/internal/walk"
)

// startingDir holds a project's starting files when its versions are git: no version contains
// them, and this folder is never saved, so the project stays clean.
const startingDir = "assets/scratch/starting/"

// replay turns each old upload into a saved version. render is the video already in the folder.
func (c *cloner) replay(plan clonePlan, render string) error {
	starting, err := c.placeStarting(plan)
	if err != nil {
		return err
	}
	if len(plan.replay) == 0 {
		return nil
	}
	if err := c.keepUploaded(plan.replay); err != nil {
		return err
	}

	var previous []string
	for _, v := range plan.replay {
		if render, previous, err = c.replayVersion(v, render, previous, starting); err != nil {
			return err
		}
	}
	return nil
}

// placeStarting places the starting version's files, and returns the ones placed at the project's root.
func (c *cloner) placeStarting(plan clonePlan) (map[string]bool, error) {
	starting := map[string]bool{}
	v := plan.starting
	if v == nil {
		return starting, nil
	}
	if len(plan.git) > 0 {
		_, err := c.placeFiles(v.Files, startingDir)
		return starting, err
	}
	listed, err := c.placeFiles(v.Files, "")
	if err != nil {
		return nil, err
	}
	for _, p := range listed {
		starting[p] = true
	}
	return starting, c.writeReadme(*v)
}

// replayVersion saves v over the version before it, whose render and files are render and previous,
// and returns v's.
func (c *cloner) replayVersion(v siteVersion, render string, previous []string, starting map[string]bool) (string, []string, error) {
	// A version with no file list has the files of the one before it, as on the site.
	if len(v.Files) > 0 {
		listed := make([]string, 0, len(v.Files))
		for _, f := range v.Files {
			listed = append(listed, f.Path)
		}
		for _, p := range previous {
			if !slices.Contains(listed, p) && !starting[p] {
				if err := c.remove(p); err != nil {
					return "", nil, err
				}
			}
		}
		previous = listed
	}
	next := ""
	if v.Render != nil {
		next = renderPath(v.Render.Path)
	}
	if render != "" && render != next && !slices.Contains(previous, render) && !starting[render] {
		if err := c.remove(render); err != nil {
			return "", nil, err
		}
	}
	if _, err := c.placeFiles(v.Files, ""); err != nil {
		return "", nil, err
	}
	if next != "" {
		if !writable(c.dir, next) {
			return "", nil, fmt.Errorf("V%d's video %s can't be written inside the project; refusing to clone it", v.N, v.Render.Path)
		}
		if err := c.place(next, v.Render.Sha256); err != nil {
			return "", nil, err
		}
	}
	if err := c.writeReadme(v); err != nil {
		return "", nil, err
	}
	return next, previous, c.saveReplayed(v, next)
}

func (c *cloner) remove(p string) error {
	if !writable(c.dir, p) {
		return nil
	}
	if err := os.Remove(filepath.Join(c.dir, p)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (c *cloner) saveReplayed(v siteVersion, render string) error {
	cfg, err := readConfig(c.dir)
	if err != nil {
		return err
	}
	// In the config, so the next `v2 save` finds this video too. v2 init's default path may not exist here.
	cfg.Render = render
	if err := writeJSON(filepath.Join(c.dir, configFile), cfg); err != nil {
		return err
	}
	title := v.Title
	if title == "" {
		title = fmt.Sprintf("Version %d", v.N)
	}
	if err := c.remember(); err != nil {
		return err
	}
	want := v.tag()
	got, err := saveVersion(c.dir, saveInput{
		message: title, prompts: v.Prompts, model: v.Model, changes: fromSite(v.Changes), render: render, replay: true,
		savedAt: time.UnixMilli(int64(v.SavedAt)).UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("V%d was saved as %s; local version numbers must match the site's", v.N, got)
	}
	return nil
}

// fromSite puts old uploads' changes in the form v2 writes: their log rows have a start and no end,
// and a cut can have no place, which log and the diff page can't show.
func fromSite(changes []change) []change {
	for i, c := range changes {
		if c.Kind == "cut" && c.From == nil {
			changes[i] = change{Text: c.Text}
		} else if c.From != nil && c.To == nil {
			changes[i].To = c.From
		}
	}
	return changes
}

// placeFiles writes what can be written and notes the rest; listed paths are returned either way.
func (c *cloner) placeFiles(files []siteFile, prefix string) ([]string, error) {
	var listed []string
	for _, f := range files {
		p := prefix + f.Path
		listed = append(listed, p)
		if f.Sha256 == "" || !store.Has(c.objects, f.Sha256) || !writable(c.dir, p) {
			c.skipped[f.Path] = true
			continue
		}
		if err := c.place(p, f.Sha256); err != nil {
			return nil, err
		}
	}
	return listed, nil
}

func (c *cloner) writeReadme(v siteVersion) error {
	if v.Readme == "" {
		return nil
	}
	// The site's readme, not an uploaded README.md, is what the page shows and the next push must carry on.
	return walk.WriteFile(filepath.Join(c.dir, "README.md"), []byte(v.Readme))
}

// renderPath keeps a render's folder when it has one; a bare name goes where renders live.
func renderPath(p string) string {
	if strings.HasPrefix(p, "/") || !strings.Contains(p, "/") {
		return "out/" + path.Base(p)
	}
	return p
}

// keepUploaded stops ignoring default-ignored folders that old uploads hold, or their files would drop out of every version.
func (c *cloner) keepUploaded(versions []siteVersion) error {
	file := filepath.Join(c.dir, ".v2ignore")
	b, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	lines := strings.Split(string(b), "\n")
	kept := slices.DeleteFunc(lines, func(line string) bool {
		if !strings.HasPrefix(line, "/") || !strings.HasSuffix(line, "/") {
			return false
		}
		return slices.ContainsFunc(versions, func(v siteVersion) bool {
			if v.Render != nil && strings.HasPrefix(v.Render.Path, line[1:]) {
				return true
			}
			return slices.ContainsFunc(v.Files, func(f siteFile) bool { return strings.HasPrefix(f.Path, line[1:]) })
		})
	})
	return walk.WriteFile(file, []byte(strings.Join(kept, "\n")))
}
