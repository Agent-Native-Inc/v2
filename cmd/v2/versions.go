package main

// A project's saved versions: the vN tags, and what each one's commit holds.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/agentnativeinc/v2/internal/gitx"
)

var versionTag = regexp.MustCompile(`^v([0-9]+)$`)

// tagOf is version n's tag.
func tagOf(n int) string {
	return "v" + strconv.Itoa(n)
}

// listVersions returns v1, v2, ... in number order (git sorts v10 before v2).
func listVersions(dir string) ([]string, error) {
	out, err := gitx.Run(dir, "tag", "--list", "v*")
	if err != nil {
		return nil, err
	}
	var numbers []int
	for tag := range strings.FieldsSeq(out) {
		if m := versionTag.FindStringSubmatch(tag); m != nil {
			n, _ := strconv.Atoi(m[1])
			numbers = append(numbers, n)
		}
	}
	slices.Sort(numbers)
	var versions []string
	for _, n := range numbers {
		versions = append(versions, tagOf(n))
	}
	return versions, nil
}

func nextVersion(dir string) (string, error) {
	versions, err := listVersions(dir)
	if err != nil {
		return "", err
	}
	if len(versions) == 0 {
		return "v1", nil
	}
	last, _ := strconv.Atoi(strings.TrimPrefix(versions[len(versions)-1], "v"))
	return tagOf(last + 1), nil
}

// commitAt reads a saved version's commit.json. Versions saved before cuts became a kind of
// change keep them in their own "cuts" list; they are folded in here so every reader sees one list.
func commitAt(dir, version string) (commitInfo, error) {
	var saved struct {
		commitInfo

		Cuts []struct {
			At      float64 `json:"at"`
			Removed float64 `json:"removed"`
			Text    string  `json:"text"`
		} `json:"cuts"`
	}
	out, err := gitx.Run(dir, "show", version+":"+commitFile)
	if err != nil {
		return commitInfo{}, err
	}
	if err := json.Unmarshal([]byte(out), &saved); err != nil {
		return commitInfo{}, fmt.Errorf("%s: bad %s: %w", version, commitFile, err)
	}
	var cuts []change
	for _, c := range saved.Cuts {
		cuts = append(cuts, change{From: &c.At, To: &c.At, Kind: "cut", Removed: c.Removed, Text: c.Text})
	}
	saved.Changes = append(cuts, saved.Changes...)
	return saved.commitInfo, nil
}

func assetsAt(dir, rev string) (map[string]asset, error) {
	assets, err := savedAssetsAt(dir, rev)
	return withoutAppleDouble(assets), err
}

// savedAssetsAt is rev's media exactly as saved, ._ files included: push and clone carry old versions
// whole, because an older v2 clones and restores only a version whose every file it can get.
func savedAssetsAt(dir, rev string) (map[string]asset, error) {
	out, err := gitx.Run(dir, "show", rev+":"+assetsFile)
	if err != nil {
		return nil, err
	}
	assets := map[string]asset{}
	if err := json.Unmarshal([]byte(out), &assets); err != nil {
		return nil, fmt.Errorf("%s: bad %s: %w", rev, assetsFile, err)
	}
	// A hash becomes a path in .v2/objects, so one like ../../.ssh/id_ed25519 would read outside it.
	for p, a := range assets {
		if !validSum.MatchString(a.Sha256) {
			return nil, fmt.Errorf("%s: bad %s: %s has hash %q", rev, assetsFile, p, a.Sha256)
		}
	}
	return assets, nil
}

type treeEntry struct{ mode, object, path string }

// lsTree lists every file in rev with its git mode and object.
func lsTree(dir, rev string) ([]treeEntry, error) {
	out, err := gitx.Run(dir, "ls-tree", "-r", "-z", rev)
	if err != nil {
		return nil, err
	}
	var entries []treeEntry
	for _, line := range gitx.SplitZ(out) {
		meta, path, ok := strings.Cut(line, "\t")
		if fields := strings.Fields(meta); ok && len(fields) == 3 {
			entries = append(entries, treeEntry{mode: fields[0], object: fields[2], path: path})
		}
	}
	return entries, nil
}
