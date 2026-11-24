package main

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/agentnativeinc/v2/internal/gitx"
)

// runLog prints every version, newest first: its title, the prompts and model, what changed,
// which recipe files changed and which binaries were added, changed or removed.
func runLog(dir string) error {
	dir, err := findProject(dir)
	if err != nil {
		return err
	}
	versions, err := listVersions(dir)
	if err != nil {
		return err
	}
	if len(versions) == 0 {
		fmt.Println("no versions yet; run `v2 save`")
		return nil
	}
	for i, v := range slices.Backward(versions) {
		if i < len(versions)-1 {
			fmt.Println()
		}
		prev := ""
		if i > 0 {
			prev = versions[i-1]
		}
		if err := printVersion(dir, v, prev); err != nil {
			return err
		}
	}
	return nil
}

// printVersion prints v, and its media changes since prev ("" for the first version: everything is added).
func printVersion(dir, v, prev string) error {
	info, err := commitAt(dir, v)
	if err != nil {
		return err
	}
	recipe, err := recipeChanges(dir, v)
	if err != nil {
		return err
	}
	now, err := assetsAt(dir, v)
	if err != nil {
		return err
	}
	before := map[string]asset{}
	if prev != "" {
		if before, err = assetsAt(dir, prev); err != nil {
			return err
		}
	}
	savedAt := strings.Replace(strings.TrimSuffix(info.SavedAt, "Z"), "T", " ", 1) + " UTC"

	fmt.Printf("%s  %s  %s\n", v, savedAt, info.Message)
	if len(info.Prompts) > 0 {
		fmt.Printf("    prompts:  \"%s\"\n", strings.Join(info.Prompts, `" ; "`))
	}
	if info.Model != "" {
		fmt.Printf("    model:    %s\n", info.Model)
	}
	fmt.Printf("    changes:  %s\n", describe(info.Changes))
	fmt.Printf("    recipe:   %s\n", orNone(recipe))
	fmt.Printf("    assets:   %s\n", orNone(assetChanges(before, now)))
	if info.Assemble != "" {
		fmt.Printf("    assemble: %s\n", info.Assemble)
	}
	if info.Render != "" {
		fmt.Printf("    render:   %s\n", info.Render)
	}
	return nil
}

// recipeChanges lists text files changed by version v, e.g. "M src/Video.tsx", leaving out v2's own files.
func recipeChanges(dir, v string) ([]string, error) {
	out, err := gitx.Run(dir, "diff-tree", "--root", "--no-commit-id", "-r", "--name-status", v, "--", ".", ":(exclude).v2")
	if err != nil {
		return nil, err
	}
	var lines []string
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if line != "" {
			lines = append(lines, strings.Replace(line, "\t", " ", 1))
		}
	}
	return lines, nil
}

// assetChanges lists media added, changed or removed between two assets.json maps, like "+ out/new.mp4".
func assetChanges(before, now map[string]asset) []string {
	var lines []string
	for _, path := range slices.Sorted(maps.Keys(now)) {
		old, existed := before[path]
		if !existed {
			lines = append(lines, "+ "+path)
		} else if old.Sha256 != now[path].Sha256 {
			lines = append(lines, "M "+path)
		}
	}
	for _, path := range slices.Sorted(maps.Keys(before)) {
		if _, kept := now[path]; !kept {
			lines = append(lines, "- "+path)
		}
	}
	return lines
}

func orNone(lines []string) string {
	if len(lines) == 0 {
		return "(none)"
	}
	return strings.Join(lines, ", ")
}
