package main

// A project folder, and the records v2 keeps for it in .v2.

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/agentnativeinc/v2/internal/walk"
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

// writeJSON writes map keys sorted, so git diffs stay small.
func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return walk.WriteFile(path, append(b, '\n'))
}

// validSum is the only shape a hash from the site may have, because it becomes a path in .v2/objects.
var validSum = regexp.MustCompile(`^([0-9a-f]{64}|unhashed:[A-Za-z0-9_-]+)$`)

// insideProject refuses paths that aren't inside the project: older uploads recorded absolute ones, and a
// stranger's project could name .git/hooks.
func insideProject(p string) bool {
	parts := strings.Split(p, "/")
	return !strings.EqualFold(parts[0], ".v2") && !slices.ContainsFunc(parts, func(part string) bool {
		return part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") || strings.Contains(part, `\`)
	})
}

// writable is insideProject, and it also refuses going through anything that isn't a real folder,
// so a link can't send a write somewhere else, like .git/config.
func writable(dir, p string) bool {
	if !insideProject(p) {
		return false
	}
	parts := strings.Split(p, "/")
	here := dir
	for i, part := range parts {
		here = filepath.Join(here, part)
		fi, err := os.Lstat(here)
		if err != nil {
			continue
		}
		last := i == len(parts)-1
		if last && !fi.Mode().IsRegular() || !last && !fi.IsDir() {
			return false
		}
	}
	return true
}
