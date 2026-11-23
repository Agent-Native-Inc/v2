package main

import (
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/Agent-Native-Inc/v2-cli/internal/walk"
)

// The fixed folders of a project made with `v2 init` in an empty folder.
var layoutDirs = []string{"assets/imported", "assets/generated", "assets/scratch", "src", "scripts", "out"}

const readme = `# Video

## What this video is

## How it was made
`

const (
	toAssets  = "media goes in assets/imported/ (files you brought: footage, music, voiceover) or assets/generated/ (made by AI or scripts)"
	toCode    = "code goes in src/ (the edit) or scripts/ (steps that make files)"
	toScratch = "media goes in assets/: results in assets/generated/, a tool's working files (partial renders, frames) in assets/scratch/<tool>/, which v2 never saves"
	toTopDirs = "only assets/, src/, scripts/ and out/ are allowed at the top of the project"
	textToSrc = "text the code reads (json, srt, csv) goes in src/, next to the code"
	toData    = "assets/ holds media only; " + textToSrc
	toOutText = "out/ holds the render only; " + textToSrc
	toLink    = "a link here isn't versioned (git skips assets/ and out/, v2 doesn't follow links); copy the file in instead, e.g. v2 import <file>"
	toNewline = "a line break in a media file's name can't be written to .gitignore (it would hide other files); rename it"
	toPublic  = "media goes in assets/; point the tool there instead of public/: Config.setPublicDir('assets') in remotion.config.ts, or publicDir: 'assets' in vite.config.ts"
)

var codeExt = map[string]bool{
	".ts": true, ".tsx": true, ".js": true, ".jsx": true, ".mjs": true, ".cjs": true,
	".py": true, ".sh": true, ".rb": true, ".go": true, ".rs": true,
}

// wouldLose: reasons where saving anyway would drop the file (git skips assets/ and out/, and v2 keeps
// only media there), so they block the save. Everything else is saved where it is, with a note.
var wouldLose = map[string]bool{toData: true, toOutText: true, toLink: true, toNewline: true}

// checkLayout returns notes on files outside the layout, and an error for files a save would lose.
func checkLayout(files []walk.File) ([]string, error) {
	// One line per folder and reason, so a misplaced build folder (Rust's target/) isn't hundreds of lines.
	type group struct{ dir, why string }
	groups := map[group][]string{}
	for _, f := range files {
		why := misplaced(f)
		if why == "" {
			continue
		}
		dir := path.Dir(f.Path)
		if why == toTopDirs || why == toPublic {
			dir, _, _ = strings.Cut(f.Path, "/")
		}
		groups[group{dir, why}] = append(groups[group{dir, why}], f.Path)
	}
	var notes, mustFix []string
	for g, paths := range groups {
		name := paths[0]
		if g.why == toNewline {
			name = strconv.Quote(name) // printed raw, the line break hides which file it is
		}
		line := fmt.Sprintf("  %s: %s", name, g.why)
		if len(paths) > 1 {
			line = fmt.Sprintf("  %s/ (%d files): %s", g.dir, len(paths), g.why)
		}
		if wouldLose[g.why] {
			mustFix = append(mustFix, line)
		} else {
			notes = append(notes, line)
		}
	}
	slices.Sort(notes)
	slices.Sort(mustFix)
	if len(mustFix) > 0 {
		return nil, fmt.Errorf("nothing saved: %d place(s) need fixing first\n%s\nFix them (and update any code that points at them), then save again", len(mustFix), strings.Join(mustFix, "\n"))
	}
	return notes, nil
}

func misplaced(f walk.File) string {
	top, rest, nested := strings.Cut(f.Path, "/")
	switch {
	case f.Binary && top != "assets" && top != "out" && strings.ContainsAny(f.Path, "\r\n"):
		return toNewline
	case f.Link && (top == "assets" || top == "out"):
		return toLink
	case !nested: // a file at the top: config, lockfiles and dotfiles only
		if strings.HasPrefix(top, ".") || top == "bun.lockb" {
			return ""
		}
		if f.Binary {
			return toAssets
		}
		if codeExt[path.Ext(top)] && !strings.Contains(top, ".config.") {
			return toCode
		}
	case strings.HasPrefix(top, "."): // .claude/, .vscode/, .github/: tool settings
	case top == "src" || top == "scripts":
		if f.Binary {
			return toScratch
		}
	case top == "assets":
		if !f.Binary {
			return toData
		}
		if !strings.HasPrefix(rest, "imported/") && !strings.HasPrefix(rest, "generated/") {
			return "put it in assets/imported/ (files you brought) or assets/generated/ (made by AI or scripts)"
		}
	case top == "out":
		if !f.Binary {
			return toOutText
		}
	case top == "public":
		return toPublic
	default:
		return toTopDirs
	}
	return ""
}
