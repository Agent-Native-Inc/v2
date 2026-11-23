package main

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Agent-Native-Inc/v2-cli/internal/gitx"
	"github.com/Agent-Native-Inc/v2-cli/internal/store"
	"github.com/Agent-Native-Inc/v2-cli/internal/walk"
)

// runSave returns the new version, or "" if nothing changed (restore relies on that).
func runSave(dir string, in saveInput) (string, error) {
	dir, err := findProject(dir)
	if err != nil {
		return "", err
	}
	if in.message == "" || len(in.changes) == 0 {
		return "", errors.New(`save needs -m "<one-line title>" and at least one --change "<when> <what changed>", e.g. --change "0:04-0:09 new b-roll"`)
	}
	if err := checkResolves(dir, in.resolves); err != nil {
		return "", err
	}
	return saveVersion(dir, in)
}

type saveInput struct {
	message  string
	prompts  []string
	model    string
	changes  []change
	assemble string
	render   string
	resolves []string
	// replay is set when clone rebuilds a version the site already has, dated savedAt. It always gets
	// its tag, because tag numbers must equal the site's; what was uploaded isn't re-judged.
	replay  bool
	savedAt string
}

func saveVersion(dir string, in saveInput) (string, error) {
	changes, assemble, render := in.changes, in.assemble, in.render
	if err := refreshAgentsFile(dir); err != nil {
		return "", err
	}

	// Flags win for this save only; the config file is the default.
	cfg, err := readConfig(dir)
	if err != nil {
		return "", err
	}
	assemble = cmp.Or(assemble, cfg.Assemble)
	render = cmp.Or(render, cfg.Render)

	files, err := walk.Scan(dir, func(path string) (string, error) { return store.Put(objectsDir(dir), path) })
	if err != nil {
		return "", err
	}
	var notes []string
	if !in.replay {
		if err := checkSources(changes, files); err != nil {
			return "", err
		}
		if notes, err = checkLayout(files); err != nil {
			return "", err
		}
	}
	if len(notes) > 0 {
		fmt.Fprintf(os.Stderr, "warning: saved, but %d place(s) are outside the project layout:\n%s\nMove them when you can (and update any code that points at them)\n", len(notes), strings.Join(notes, "\n"))
	}

	oldAssets, err := readAssets(dir)
	if err != nil {
		return "", err
	}
	assets, err := storeMedia(dir, files)
	if err != nil {
		return "", err
	}
	if err := writeJSON(filepath.Join(dir, assetsFile), assets); err != nil {
		return "", err
	}
	if err := writeExclude(dir, slices.Collect(maps.Keys(assets))); err != nil {
		return "", err
	}

	if _, err := gitx.Run(dir, "add", "-A"); err != nil {
		return "", err
	}
	nested, err := nestedRepos(dir)
	if err != nil {
		return "", err
	}
	if len(nested) > 0 {
		_, _ = gitx.Run(dir, "reset", "-q")
		return "", fmt.Errorf("nothing saved: %s has its own git repository (a starter ran git init there), so its files wouldn't be saved; delete %s/.git and save again", nested[0], nested[0])
	}
	status, err := gitx.Run(dir, "status", "--porcelain")
	if err != nil {
		return "", err
	}
	if status == "" && !in.replay {
		return "", nothingChanged(dir, in, cfg, assets)
	}

	version, err := nextVersion(dir)
	if err != nil {
		return "", err
	}
	render = checkRender(render, assets, oldAssets, version)
	if assemble == "" && render != "" && !in.replay {
		fmt.Fprintf(os.Stderr, "warning: no assembly command recorded; set \"assemble\" in %s or pass --assemble\n", configFile)
	}
	info := commitInfo{
		Version:  version,
		Message:  in.message,
		Prompts:  in.prompts,
		Model:    in.model,
		Changes:  changes,
		Assemble: assemble,
		Render:   render,
		SavedAt:  cmp.Or(in.savedAt, time.Now().UTC().Format(time.RFC3339)),
	}
	if err := writeJSON(filepath.Join(dir, commitFile), info); err != nil {
		return "", err
	}

	if _, err := gitx.Run(dir, "add", commitFile); err != nil {
		return "", err
	}
	commit := append(fallbackIdentity(dir), "commit", "-q", "-m", version+": "+in.message, "-m", describe(changes))
	if _, err := gitx.Run(dir, commit...); err != nil {
		return "", err
	}
	if _, err := gitx.Run(dir, "tag", version); err != nil {
		return "", err
	}
	if err := settleComments(dir, version, in.resolves); err != nil {
		return "", fmt.Errorf("saved %s, but the comments weren't updated: %w", version, err)
	}
	fmt.Printf("saved %s (%d files, %d binaries)\n", version, len(files), len(assets))
	tellToPush(dir)
	return version, nil
}

// tellToPush says to push when the project has a link. An agent in a new session knows of the link
// only from this line, and treated softer wording as something to offer the user.
func tellToPush(dir string) {
	if id, _ := gitx.Run(dir, "config", "--get", "v2.video"); strings.TrimSpace(id) != "" {
		fmt.Println("this project has a link on v2.video, which the user asked for earlier: run `v2 push` now so it shows this version, don't ask first")
	}
}

// nothingChanged ends a save that found no file to save. It makes no version, so anything the flags
// asked for is recorded against the last version or the save fails: exit 0 must never mean "dropped".
func nothingChanged(dir string, in saveInput, cfg config, assets map[string]asset) error {
	render := filepath.ToSlash(in.render)
	if _, ok := assets[render]; !ok {
		render = ""
	}
	if in.assemble == "" && render == "" && len(in.resolves) == 0 {
		fmt.Println("nothing changed since the last save")
		return nil
	}
	versions, err := listVersions(dir)
	if err != nil || len(versions) == 0 {
		fmt.Println("nothing changed since the last save")
		return err
	}
	last := versions[len(versions)-1]
	info, err := commitAt(dir, last)
	if err != nil {
		return err
	}
	// A version's commit.json is never rewritten, and a pushed one is already on the site.
	// A flag the config already holds can't be fixed by setting it: the agent must drop it instead.
	var flags, keys, held []string
	unrecorded := func(flag, key, got, recorded, inConfig string) {
		switch got {
		case "", recorded:
		case inConfig:
			held = append(held, flag)
		default:
			flags, keys = append(flags, flag), append(keys, key)
		}
	}
	unrecorded("--render", `"render"`, render, info.Render, filepath.ToSlash(cfg.Render))
	unrecorded("--assemble", `"assemble"`, in.assemble, info.Assemble, cfg.Assemble)
	if len(flags) > 0 {
		return fmt.Errorf("nothing saved: no file changed since %s, so %s %s NOT recorded; set %s in %s and save again", last, strings.Join(flags, " and "), wasWere(flags), strings.Join(keys, " and "), configFile)
	}
	if len(held) > 0 {
		return fmt.Errorf("nothing saved: no file changed since %s, so %s %s NOT recorded, and %s already says the same; save again without %s (the next save that changes a file records it)", last, strings.Join(held, " and "), wasWere(held), configFile, strings.Join(held, " and "))
	}
	if len(in.resolves) == 0 {
		fmt.Println("nothing changed since the last save")
		return nil
	}
	return resolveInLast(dir, last, in.resolves)
}

func wasWere(flags []string) string {
	if len(flags) > 1 {
		return "were"
	}
	return "was"
}

// fallbackIdentity is the identity to commit as when git has none and can't guess one, as on a Linux
// box whose hostname has no domain; nil when git has one.
func fallbackIdentity(dir string) []string {
	if _, err := gitx.Run(dir, "var", "GIT_COMMITTER_IDENT"); err == nil {
		return nil
	}
	return []string{"-c", "user.name=v2", "-c", "user.email=v2@localhost"}
}

func storeMedia(dir string, files []walk.File) (map[string]asset, error) {
	objects := objectsDir(dir)
	assets := map[string]asset{}
	for _, f := range files {
		if !f.Binary {
			continue
		}
		// The hash cache can know a file whose object is gone (a wiped store): store it again.
		if !store.Has(objects, f.Sha256) {
			if _, err := store.Put(objects, filepath.Join(dir, f.Path)); err != nil {
				return nil, err
			}
		}
		source := "derived"
		if strings.HasPrefix(f.Path, "assets/imported/") {
			source = "imported"
		}
		assets[f.Path] = asset{f.Sha256, f.Size, source}
	}
	return assets, nil
}

// nestedRepos lists staged folders that are git repositories of their own:
// git would store a pointer to that repository instead of its files.
func nestedRepos(dir string) ([]string, error) {
	staged, err := gitx.Run(dir, "ls-files", "--stage")
	if err != nil {
		return nil, err
	}
	const gitlinkMode = "160000 "
	var nested []string
	for line := range strings.SplitSeq(staged, "\n") {
		if strings.HasPrefix(line, gitlinkMode) {
			_, path, _ := strings.Cut(line, "\t")
			nested = append(nested, path)
		}
	}
	return nested, nil
}

// checkRender warns when the last version had a render, or out/ has a video under another name:
// early saves have none yet.
func checkRender(render string, assets, oldAssets map[string]asset, version string) string {
	if render == "" {
		return ""
	}
	render = filepath.ToSlash(render)
	if _, ok := assets[render]; ok {
		return render
	}
	paths := slices.Sorted(maps.Keys(assets))
	other := slices.IndexFunc(paths, func(p string) bool { return strings.HasPrefix(p, "out/") })
	switch _, had := oldAssets[render]; {
	case had:
		fmt.Fprintf(os.Stderr, "warning: the render %s was in the last version but is missing now, so %s has no video; render it and save again\n", render, version)
	case other >= 0:
		fmt.Fprintf(os.Stderr, "warning: %s has no video: the render is set to %s, which doesn't exist, but %s does; if that's the render, set \"render\" to it in %s and save again\n", version, render, paths[other], configFile)
	}
	return ""
}

// checkSources refuses a --change that names a file this version doesn't have,
// so the versions page never points at something that isn't there.
func checkSources(changes []change, files []walk.File) error {
	have := map[string]bool{}
	for _, f := range files {
		have[f.Path] = true
	}
	for _, c := range changes {
		if c.Src != "" && !have[filepath.ToSlash(filepath.Clean(c.Src))] {
			return fmt.Errorf("nothing saved: --change names %s, which isn't in the project; use the file's path from the project folder, e.g. src/Titles.tsx or assets/generated/vo.mp3", c.Src)
		}
	}
	return nil
}
