package main

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Agent-Native-Inc/v2/internal/gitx"
	"github.com/Agent-Native-Inc/v2/internal/walk"
)

//go:embed agents_section.md
var agentsSection string

// v2ignore: what new projects never save. Tool caches, scratch output, and secrets; renders
// in output/ or renders/ aren't here, so save points them to out/ instead of losing them.
const v2ignore = `# Never saved by v2 (same format as .gitignore). Add tool caches and scratch output here.
node_modules/
__pycache__/
.venv/
venv/
.env
.env.*
.envrc
.npmrc
.netrc
*.pem
/dist/
/build/
/.next/
.cache/
/media/
assets/scratch/
target/
.hyperframes/
editly-tmp-*/
ffmpeg2pass-*
*TEMP_MPY_*
.DS_Store
*.v2tmp
`

// runInit makes dir a v2 project with the standard folders. Safe to run again, and safe in an
// existing git repo: it never touches history, and existing files stay where they are.
func runInit(dir string) error {
	if err := checkPlace(dir); err != nil {
		return err
	}
	if err := setUpProject(dir); err != nil {
		return err
	}
	abs, _ := filepath.Abs(dir)
	fmt.Println("v2 project ready in", abs)
	fmt.Println("media goes in assets/, the edit in src/, steps in scripts/, the render in out/")
	return nil
}

func setUpProject(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); os.IsNotExist(err) {
		if _, err := gitx.Run(dir, "init", "-q"); err != nil {
			return err
		}
	} else if _, err := gitx.Run(dir, "--version"); err != nil {
		return err
	}
	for _, d := range append(layoutDirs, ".v2/objects") {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			return err
		}
	}
	for path, content := range map[string]string{
		assetsFile:  "{}\n",
		configFile:  "{\n  \"assemble\": \"\",\n  \"render\": \"out/final.mp4\"\n}\n",
		".v2ignore": v2ignore,
		"README.md": readme,
	} {
		if err := writeIfMissing(filepath.Join(dir, path), content); err != nil {
			return err
		}
	}
	if err := writeExclude(dir, nil); err != nil { // keep .v2ignore'd files out of git from the start
		return err
	}

	// Agents learn v2 from AGENTS.md (Codex reads it); CLAUDE.md imports it for Claude Code.
	if err := writeSection(filepath.Join(dir, "AGENTS.md"), agentsSection); err != nil {
		return err
	}
	return appendOnce(filepath.Join(dir, "CLAUDE.md"), "@AGENTS.md", "@AGENTS.md\n")
}

func refreshAgentsFile(dir string) error {
	path := filepath.Join(dir, "AGENTS.md")
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) || err == nil && !strings.Contains(string(b), "<!-- v2: start -->") {
		return nil
	}
	if err != nil {
		return err
	}
	return writeSection(path, agentsSection)
}

func writeIfMissing(path, content string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if os.IsExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = f.WriteString(content)
	return errors.Join(err, f.Close())
}

// writeSection puts v2's section into path, replacing an older copy between the markers, so
// running `v2 init` again after upgrading gives agents the current instructions.
// Everything outside the markers is the user's and stays as it is.
func writeSection(path, section string) error {
	const start, end = "<!-- v2: start -->", "<!-- v2: end -->"
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	s := string(b)
	i, j := strings.Index(s, start), strings.Index(s, end)
	if i < 0 || j < i {
		return appendOnce(path, start, section)
	}
	s = s[:i] + strings.TrimSuffix(section, "\n") + s[j+len(end):]
	return walk.WriteFile(path, []byte(s))
}

// appendOnce adds text to the end of path unless path already contains marker.
// The user's own content in the file is never changed.
func appendOnce(path, marker, text string) error {
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if strings.Contains(string(b), marker) {
		return nil
	}
	if len(b) > 0 {
		text = "\n" + text
		if !strings.HasSuffix(string(b), "\n") {
			text = "\n" + text
		}
	}
	return walk.WriteFile(path, append(b, text...))
}

// checkPlace refuses a new project in a folder that holds far more than one video: the disk root,
// the folder of home folders, the temp folder, home and the folders a desktop puts in it. An agent
// in the wrong folder runs init there, and v2 would start saving everything in it. Folders inside
// them are fine. It compares the folders themselves, so a symlink or a Mac's ~/desktop for
// ~/Desktop is caught too.
func checkPlace(dir string) error {
	here, err := os.Stat(dir)
	if err != nil {
		return err
	}
	broad := []string{"/", "/Users", "/home", "/tmp", os.TempDir()}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		broad = append(broad, home)
		for _, d := range []string{"Desktop", "Documents", "Downloads", "Movies", "Music", "Pictures"} {
			broad = append(broad, filepath.Join(home, d))
		}
	}
	for _, b := range broad {
		if fi, err := os.Stat(b); err == nil && os.SameFile(here, fi) {
			abs, _ := filepath.Abs(dir)
			return fmt.Errorf("v2 won't track all of %s; make a folder for this video (mkdir my-video && cd my-video) and run v2 init there", abs)
		}
	}
	return nil
}
