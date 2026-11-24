package main

// v2 push: send saved versions to v2.video. Talks to the site's Convex functions (convex/cli.ts)
// over Convex's HTTP API. Media goes up once per user, keyed by sha256.

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Agent-Native-Inc/v2/internal/gitx"
)

var heading = regexp.MustCompile(`(?m)^#\s+(.+)$`)

func runPush(dir string) error {
	dir, err := findProject(dir)
	if err != nil {
		return err
	}
	l, err := readLogin()
	if err != nil {
		return err
	}
	versions, err := listVersions(dir)
	if err != nil {
		return err
	}
	if len(versions) == 0 {
		return errors.New("nothing to push; save a version first")
	}
	tmp, err := os.MkdirTemp("", "v2push")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	gitDir, err := gitx.Run(dir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return err
	}
	gitDir = strings.TrimSpace(gitDir)

	b := pushBuild{dir: dir, objects: objectsDir(dir), tmp: tmp, gitDir: gitDir, blobs: map[string]blobSource{}, code: map[string]pushFile{}, noCopy: map[string]bool{}}
	var payload []pushVersion
	prev := ""
	// Rebuilds every version's bundle and poster on each push; skip versions the site has if pushes get slow.
	for _, v := range versions {
		pv, err := b.version(v, prev)
		if err != nil {
			return err
		}
		payload = append(payload, pv)
		prev = v
	}

	videoID := gitx.Config(dir, "v2.video")
	forkOf := gitx.Config(dir, "v2.forkOf")
	args, err := pushArgs(dir, l, payload, videoID, forkOf)
	if err != nil {
		return err
	}
	missing, err := missingBlobs(l, slices.Collect(maps.Keys(b.blobs)), videoID == "" && forkOf != "", forkOf)
	if err != nil {
		return err
	}
	// The site names a new project before it looks at any file, so this push tells a taken name
	// before anything goes up. With the name free, the site refuses the files it lacks and keeps nothing.
	// Knows the refusal by its first words; a reworded one arrives after the upload again. A name query on the site would end that.
	if videoID == "" && forkOf == "" && len(missing) > 0 {
		if err := l.call("mutation", "cli:push", args, nil); err != nil && strings.HasPrefix(err.Error(), "You already have a project called") {
			return err
		}
	}
	if err := uploadMissing(l, missing, b.blobs, gitDir); err != nil {
		return err
	}

	var out struct {
		VideoID string    `json:"videoId"`
		URL     string    `json:"url"`
		Pushed  []float64 `json:"pushed"` // Convex sends every number as a float: [1.0, 2.0]
	}
	if err := l.call("mutation", "cli:push", args, &out); err != nil {
		return err
	}
	// Kept in .git/config: never part of a version, so restoring one can't lose it.
	if _, err := gitx.Run(dir, "config", "v2.video", out.VideoID); err != nil {
		return err
	}
	if err := sendSiteResolves(dir, l, out.VideoID); err != nil {
		return fmt.Errorf("pushed, but the fixed comments are still open on the site, the next push retries them: %w", err)
	}
	if len(out.Pushed) == 0 {
		fmt.Printf("already up to date: %s\n", l.Site+out.URL)
		return nil
	}
	fmt.Printf("pushed v%g-v%g: %s\n", out.Pushed[0], out.Pushed[len(out.Pushed)-1], l.Site+out.URL)
	return nil
}

// pushArgs is cli:push's arguments: the project it updates, or the title of a new one and the
// version of the project it forks.
func pushArgs(dir string, l login, payload []pushVersion, videoID, forkOf string) (map[string]any, error) {
	args := l.authed(map[string]any{"versions": payload})
	if videoID != "" {
		args["videoId"] = videoID
		return args, nil
	}
	args["title"] = projectTitle(dir, payload[len(payload)-1].Readme)
	if forkOf != "" {
		forkN, err := strconv.Atoi(gitx.Config(dir, "v2.forkN"))
		if err != nil {
			return nil, fmt.Errorf("v2.forkN in .git/config isn't the forked version's number: %w", err)
		}
		args["forkOf"] = map[string]any{"videoId": forkOf, "n": forkN}
	}
	return args, nil
}

// missingBlobs returns the hashes the site doesn't have, after sharing a fork's blobs from the
// project forkOf when share is set.
func missingBlobs(l login, hashes []string, share bool, forkOf string) ([]string, error) {
	if share {
		for batch := range slices.Chunk(hashes, blobBatch) {
			if err := l.call("mutation", "cli:shareBlobs", l.authed(map[string]any{"from": forkOf, "hashes": batch}), nil); err != nil {
				return nil, err
			}
		}
	}
	var missing []string
	for batch := range slices.Chunk(hashes, blobBatch) {
		var some []string
		if err := l.call("query", "cli:missingBlobs", l.authed(map[string]any{"hashes": batch}), &some); err != nil {
			return nil, err
		}
		missing = append(missing, some...)
	}
	return missing, nil
}

// projectTitle names a new video on the site: README's first heading, else the folder's name.
func projectTitle(dir, readme string) string {
	if m := heading.FindStringSubmatch(readme); m != nil && strings.TrimSpace(m[1]) != "Video" {
		return strings.TrimSpace(m[1])
	}
	if title := gitx.Config(dir, "v2.forkTitle"); title != "" {
		return title
	}
	abs, _ := filepath.Abs(dir)
	return filepath.Base(abs)
}
