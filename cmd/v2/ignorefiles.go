package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/agentnativeinc/v2/internal/gitx"
	"github.com/agentnativeinc/v2/internal/walk"
)

const (
	blockStart        = "# stored by v2, not git (written by v2 save, don't edit)"
	blockEnd          = "# end v2"
	restoreBlockStart = "# v2 restore: ignored before the last restore (managed by v2)"
	restoreBlockEnd   = "# end v2 restore"
)

var alwaysIgnored = []string{".v2/objects/", ".v2/hashcache", ".v2/hashcache.tmp", ".v2/clock", "._*"}

// .gitignore gets what v2 stores instead of git and travels with the project;
// .git/info/exclude gets the .v2ignore patterns and stays local.
func writeExclude(dir string, mediaPaths []string) error {
	stored := append(slices.Clone(alwaysIgnored), "/assets/", "/out/")
	slices.Sort(mediaPaths)
	for _, p := range mediaPaths {
		if !strings.HasPrefix(p, "assets/") && !strings.HasPrefix(p, "out/") {
			stored = append(stored, "/"+escapeGitignore(p))
		}
	}
	v2ignore, err := walk.Ignore(dir)
	if err != nil {
		return err
	}
	exclude, err := gitInfoExclude(dir)
	if err != nil {
		return err
	}
	if err := writeBlock(filepath.Join(dir, ".gitignore"), blockStart, blockEnd, stored); err != nil {
		return err
	}
	return writeBlock(exclude, blockStart, blockEnd, v2ignore)
}

func keepIgnored(dir string, paths []string) error {
	exclude, err := gitInfoExclude(dir)
	if err != nil {
		return err
	}
	var lines []string
	for _, p := range paths {
		if p != "" {
			lines = append(lines, "/"+escapeGitignore(p))
		}
	}
	return writeBlock(exclude, restoreBlockStart, restoreBlockEnd, lines)
}

// writeBlock replaces the lines between start and end in path, keeping every other line.
// If the end marker was deleted by hand, the lines after start are kept, never dropped.
func writeBlock(path, start, end string, block []string) error {
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var kept, inside []string
	inBlock := false
	for line := range strings.SplitSeq(strings.TrimRight(string(b), "\n"), "\n") {
		switch {
		case line == start:
			inBlock, inside = true, inside[:0]
		case line == end:
			inBlock, inside = false, inside[:0]
		case inBlock:
			inside = append(inside, line)
		default:
			kept = append(kept, line)
		}
	}
	kept = append(kept, inside...)
	if len(kept) == 1 && kept[0] == "" {
		kept = nil
	}
	kept = append(kept, start)
	kept = append(kept, block...)
	kept = append(kept, end)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return walk.WriteFile(path, []byte(strings.Join(kept, "\n")+"\n"))
}

// gitInfoExclude asks git where the exclude file is, so this also works in worktrees where .git is a file.
func gitInfoExclude(dir string) (string, error) {
	out, err := gitx.Run(dir, "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return "", err
	}
	exclude := strings.TrimSpace(out)
	if !filepath.IsAbs(exclude) {
		exclude = filepath.Join(dir, exclude)
	}
	return exclude, nil
}

// escapeGitignore makes a path match literally, even if it contains * ? [ \ or trailing spaces.
func escapeGitignore(p string) string {
	var sb strings.Builder
	for _, r := range p {
		if strings.ContainsRune(`\*?[`, r) {
			sb.WriteRune('\\')
		}
		sb.WriteRune(r)
	}
	escaped := sb.String()
	trimmed := strings.TrimRight(escaped, " ")
	trailingSpaces := len(escaped) - len(trimmed)
	return trimmed + strings.Repeat(`\ `, trailingSpaces)
}
