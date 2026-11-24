// Package gitx runs the installed git command.
package gitx

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os/exec"
	"path"
	"slices"
	"strings"
	"sync"

	"github.com/agentnativeinc/v2/internal/platform"
)

func errNoGit() error {
	return errors.New("v2 needs git, but it isn't installed. " + platform.InstallHint("git"))
}

// Run runs git with args inside dir and returns its stdout.
// On failure the error carries git's own message, prefixed with the subcommand.
func Run(dir string, args ...string) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", errNoGit()
	}
	// Housekeeping after fetch or commit runs in the foreground, so no git outlives v2 and writes into a folder
	// v2 renames or deletes next (a clone's .part).
	cmd := exec.Command("git", append([]string{"-c", "gc.autoDetach=false", "-c", "maintenance.autoDetach=false"}, args...)...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		// On a Mac without Command Line Tools, /usr/bin/git exists but is only a stub that fails like this.
		if strings.Contains(msg, "xcrun: error") {
			return "", errNoGit()
		}
		if msg == "" {
			msg = strings.TrimSpace(stdout.String()) // e.g. "nothing to commit" goes to stdout
		}
		if msg == "" {
			msg = err.Error() // git never ran, e.g. "argument list too long"
		}
		return "", fmt.Errorf("git %s: %s", args[0], msg)
	}
	return stdout.String(), nil
}

// IgnoredOthers lists the untracked paths that exclude (a git ls-files option, like
// --exclude-standard) covers. A folder ignored whole comes back as one "dir/" entry,
// so its files aren't listed and the folder is never opened. The exception is git before
// 2.32, where an ignored folder inside an untracked folder is found file by file first.
func IgnoredOthers(dir, exclude string) ([]string, error) {
	old := make(chan bool, 1) // asked beside the listing, so current git doesn't wait for it
	go func() { old <- directoryMissesNested() }()
	out, err := Run(dir, "ls-files", "-z", "--others", "--ignored", exclude, "--directory")
	if err != nil {
		return nil, err
	}
	paths := SplitZ(out)
	if !<-old {
		return paths, nil
	}
	// With --directory, git before 2.32 reports nothing inside an untracked folder, so assets/scratch/
	// in assets/ would be missed. Those folders get a file-by-file listing instead.
	out, err = Run(dir, "ls-files", "-z", "--others", exclude, "--directory")
	if err != nil {
		return nil, err
	}
	folders := map[string]bool{}
	for _, p := range SplitZ(out) {
		if folder, ok := strings.CutSuffix(p, "/"); ok {
			folders[folder] = true
		}
	}
	files, err := lsFiles(dir, slices.Sorted(maps.Keys(folders)), "--others", "--ignored", exclude)
	if err != nil {
		return nil, err
	}
	// Old git does answer about an ignored folder asked for by name. So each file's folders inside
	// the untracked one are asked about from the top down, and a file in a folder ignored whole
	// gives way to that folder: remotion/node_modules/ is one entry, not its 80,000 files.
	// Asking one level at a time also keeps git 2.25 from failing with "directory entry not
	// superset of prefix" when every name in a call sits inside an ignored folder.
	next := map[string]int{} // file -> how many of its path's names make the next folder to ask about
	for _, f := range files {
		d := path.Dir(f)
		for d != "." && !folders[d] {
			d = path.Dir(d)
		}
		next[f] = 1
		if d != "." {
			next[f] = strings.Count(d, "/") + 2
		}
	}
	found := map[string]bool{}
	for len(next) > 0 {
		ask := map[string]bool{}
		for f, n := range next {
			if names := strings.Split(f, "/"); n < len(names) {
				ask[strings.Join(names[:n], "/")] = true
			} else {
				found[f] = true
				delete(next, f)
			}
		}
		whole, err := lsFiles(dir, slices.Sorted(maps.Keys(ask)), "--others", "--ignored", exclude, "--directory")
		if err != nil {
			return nil, err
		}
		ignored := map[string]bool{}
		for _, d := range whole {
			ignored[strings.TrimSuffix(d, "/")] = true
		}
		for f, n := range next {
			if d := strings.Join(strings.Split(f, "/")[:n], "/"); ignored[d] {
				found[d+"/"] = true
				delete(next, f)
			} else {
				next[f] = n + 1
			}
		}
	}
	return append(paths, slices.Sorted(maps.Keys(found))...), nil
}

// lsFiles runs git ls-files with opts on the folders, in batches that stay under the
// operating system's limit on command line length.
func lsFiles(dir string, folders []string, opts ...string) ([]string, error) {
	var paths []string
	for len(folders) > 0 {
		args := append([]string{"ls-files", "-z"}, opts...)
		args = append(args, "--")
		for size := 0; len(folders) > 0 && size < 100_000; folders = folders[1:] {
			args = append(args, ":(literal)"+folders[0])
			size += len(folders[0]) + 20
		}
		out, err := Run(dir, args...)
		if err != nil {
			return nil, err
		}
		paths = append(paths, SplitZ(out)...)
	}
	return paths, nil
}

// Config returns git config key in dir, trimmed; "" when it isn't set.
func Config(dir, key string) string {
	out, _ := Run(dir, "config", "--get", key)
	return strings.TrimSpace(out)
}

// SplitZ splits the output of a git command run with -z into its NUL-terminated entries.
func SplitZ(out string) []string {
	if out == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
}

var directoryMissesNested = sync.OnceValue(func() bool {
	out, err := Run(".", "version")
	var major, minor int
	if _, scanErr := fmt.Sscanf(out, "git version %d.%d", &major, &minor); err != nil || scanErr != nil {
		return true // the file-by-file listing is right on every git, just slower
	}
	return major < 2 || major == 2 && minor < 32
})
