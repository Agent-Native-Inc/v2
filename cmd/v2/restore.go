package main

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/agentnativeinc/v2/internal/gitx"
	"github.com/agentnativeinc/v2/internal/store"
	"github.com/agentnativeinc/v2/internal/walk"
)

// runRestore brings the folder back to version v exactly, then saves that as a new version.
// It refuses to run while there are unsaved changes.
// History only moves forward: every version stays restorable.
func runRestore(dir, v string) error {
	dir, err := findProject(dir)
	if err != nil {
		return err
	}
	versions, err := listVersions(dir)
	if err != nil {
		return err
	}
	if !slices.Contains(versions, v) {
		return fmt.Errorf("no version %s; run `v2 log` to see versions", v)
	}
	if err := refuseUnsaved(dir, v); err != nil {
		return err
	}

	current, err := readAssets(dir)
	if err != nil {
		return err
	}
	target, err := assetsAt(dir, v)
	if err != nil {
		return err
	}
	// v's render and assemble may have come from flags, which its config doesn't hold.
	was, err := commitAt(dir, v)
	if err != nil {
		return err
	}
	// Media is fetched first, so nothing in the folder changes if an object is missing or corrupt.
	fetched, err := fetchMedia(dir, v, current, target)
	defer removeTemps(fetched)
	if err != nil {
		return err
	}

	// What git ignores now (a .env with API keys, caches) must stay ignored after v's older
	// .gitignore comes back, or the restore's save would commit it.
	ignoredNow, err := gitx.IgnoredOthers(dir, "--exclude-standard")
	if err != nil {
		return err
	}
	// .v2ignore only grows: an older version's copy must not stop ignoring what's ignored now.
	v2ignore, err := os.ReadFile(filepath.Join(dir, ".v2ignore"))
	hadV2ignore := err == nil
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	if err := removeMedia(dir, current, target); err != nil {
		return err
	}
	// --no-overlay is restore's default: text files that v doesn't have are removed too. The Mac's ._
	// files an old version tracks are left as they are, like clone leaves them.
	if _, err := gitx.Run(dir, "restore", "--source="+v, "--staged", "--worktree", "--", ".", ":(exclude,glob)**/._*", ":(exclude,glob)**/._*/**"); err != nil {
		return err
	}
	restored := map[string]string{}
	for path, tmp := range fetched {
		if err := os.Rename(tmp, filepath.Join(dir, path)); err != nil {
			return fmt.Errorf("restore of %s stopped halfway at %s (run `v2 restore` with your latest version to undo): %w", v, path, err)
		}
		restored[path] = target[path].Sha256
	}
	if err := walk.Remember(dir, restored); err != nil {
		return err
	}
	if hadV2ignore {
		if err := walk.WriteFile(filepath.Join(dir, ".v2ignore"), v2ignore); err != nil {
			return err
		}
	}
	if err := keepIgnored(dir, ignoredNow); err != nil {
		return err
	}

	newVersion, err := runSave(dir, saveInput{message: "restore " + v, changes: []change{{Text: "restored " + v + " exactly"}}, assemble: was.Assemble, render: was.Render})
	if err != nil {
		return err
	}
	if newVersion == "" {
		fmt.Printf("already at %s's state; nothing to restore\n", v)
	} else {
		fmt.Printf("restored %s (saved as %s)\n", v, newVersion)
	}
	return nil
}

func refuseUnsaved(dir, v string) error {
	dirty, err := unsaved(dir)
	if err != nil {
		return err
	}
	if len(dirty) == 0 {
		return nil
	}
	shown := dirty
	if len(shown) > 10 {
		shown = append(slices.Clone(dirty[:10]), fmt.Sprintf("... and %d more", len(dirty)-10))
	}
	return fmt.Errorf("can't restore %s: %d unsaved change(s) would be lost:\n  %s\n"+
		"Save them first with `v2 save -m \"<title>\" --prompt \"<user's original message>\" --change \"<what changed>\"`, then run `v2 restore %s` again.",
		v, len(dirty), strings.Join(shown, "\n  "), v)
}

func fetchMedia(dir, v string, current, target map[string]asset) (map[string]string, error) {
	fetched := map[string]string{}
	for _, path := range slices.Sorted(maps.Keys(target)) {
		if current[path].Sha256 == target[path].Sha256 {
			continue
		}
		if !writable(dir, path) {
			return fetched, fmt.Errorf("can't restore %s, nothing was changed: %s goes through a link or isn't inside the project", v, path)
		}
		tmp := filepath.Join(dir, path) + ".restore.v2tmp"
		if err := store.Get(objectsDir(dir), target[path].Sha256, tmp); err != nil {
			return fetched, fmt.Errorf("can't restore %s, nothing was changed: %s: %w", v, path, err)
		}
		fetched[path] = tmp
	}
	return fetched, nil
}

func removeMedia(dir string, current, target map[string]asset) error {
	for path := range current {
		if _, kept := target[path]; kept {
			continue
		}
		if err := os.Remove(filepath.Join(dir, path)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func removeTemps(tmps map[string]string) {
	for _, tmp := range tmps {
		_ = os.Remove(tmp)
	}
}

// unsaved lists what changed since the last save, like "M src/Video.tsx" or "+ out/new.mp4".
func unsaved(dir string) ([]string, error) {
	saved, err := readAssets(dir)
	if err != nil {
		return nil, err
	}
	files, err := walk.Scan(dir, store.Hash) // hash only; nothing is stored
	if err != nil {
		return nil, err
	}
	onDisk := map[string]asset{}
	for _, f := range files {
		if f.Binary {
			onDisk[f.Path] = asset{Sha256: f.Sha256}
		}
	}
	changes := assetChanges(saved, onDisk)

	// A rename or copy (R or C) is followed by one more entry: the old path.
	out, err := gitx.Run(dir, "status", "--porcelain", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	entries := gitx.SplitZ(out)
	for i := 0; i < len(entries); i++ {
		entry := entries[i]
		if len(entry) < 4 {
			continue
		}
		code, path := strings.TrimSpace(entry[:2]), entry[3:]
		if code[0] == 'R' || code[0] == 'C' {
			i++
		}
		_, isMedia := onDisk[path]
		_, isAsset := saved[path]
		if !isMedia && !isAsset && !walk.AppleDouble(path) {
			changes = append(changes, code+" "+path)
		}
	}
	return changes, nil
}
