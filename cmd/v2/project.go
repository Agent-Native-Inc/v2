package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Agent-Native-Inc/v2-cli/internal/gitx"
	"github.com/Agent-Native-Inc/v2-cli/internal/walk"
)

const (
	assetsFile = ".v2/assets.json"
	commitFile = ".v2/commit.json"
	configFile = ".v2/config.json"
)

type asset struct {
	Sha256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Source string `json:"source"`
}

type config struct {
	Assemble string `json:"assemble"`
	Render   string `json:"render"`
}

type commitInfo struct {
	Version  string   `json:"version"`
	Message  string   `json:"message"`
	Prompts  []string `json:"prompts,omitempty"`
	Model    string   `json:"model,omitempty"`
	Changes  []change `json:"changes"`
	Assemble string   `json:"assemble"`
	Render   string   `json:"render,omitempty"`
	SavedAt  string   `json:"saved_at"`
}

// findProject returns the project folder that holds dir, looking up from dir like git does.
func findProject(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for d := abs; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, assetsFile)); err == nil {
			return d, nil
		}
		if d == filepath.Dir(d) {
			return "", errors.New("not a v2 project here; run `v2 init` first")
		}
	}
}

func objectsDir(dir string) string {
	return filepath.Join(dir, ".v2", "objects")
}

func readAssets(dir string) (map[string]asset, error) {
	b, err := os.ReadFile(filepath.Join(dir, assetsFile))
	if err != nil {
		return nil, err
	}
	assets := map[string]asset{}
	if err := json.Unmarshal(b, &assets); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", assetsFile, err)
	}
	return withoutAppleDouble(assets), nil
}

// withoutAppleDouble drops the Mac's ._ files, which versions saved before v2 skipped them list as media.
// Restoring one can stop a restore halfway: the Mac moves and deletes them along with their partners.
func withoutAppleDouble(assets map[string]asset) map[string]asset {
	maps.DeleteFunc(assets, func(path string, _ asset) bool { return walk.AppleDouble(path) })
	return assets
}

func readConfig(dir string) (config, error) {
	var cfg config
	return cfg, readJSON(filepath.Join(dir, configFile), &cfg)
}

// readJSON decodes the file at path into v, and leaves v as it is when there's no such file.
func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s is not valid JSON: %w", path, err)
	}
	return nil
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
	return assets, nil
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

type treeEntry struct{ mode, object, path string }

// lsTree lists every file in rev with its git mode and object.
func lsTree(dir, rev string) ([]treeEntry, error) {
	out, err := gitx.Run(dir, "ls-tree", "-r", "-z", rev)
	if err != nil {
		return nil, err
	}
	var entries []treeEntry
	for line := range strings.SplitSeq(strings.TrimSuffix(out, "\x00"), "\x00") {
		meta, path, ok := strings.Cut(line, "\t")
		if fields := strings.Fields(meta); ok && len(fields) == 3 {
			entries = append(entries, treeEntry{mode: fields[0], object: fields[2], path: path})
		}
	}
	return entries, nil
}

var versionTag = regexp.MustCompile(`^v([0-9]+)$`)

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
		versions = append(versions, "v"+strconv.Itoa(n))
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
	return "v" + strconv.Itoa(last+1), nil
}

// writeJSON writes map keys sorted, so git diffs stay small.
func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
