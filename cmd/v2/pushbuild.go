package main

// What a push sends: each version's payload, and every blob the payloads name.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Agent-Native-Inc/v2/internal/gitx"
	"github.com/Agent-Native-Inc/v2/internal/media"
	"github.com/Agent-Native-Inc/v2/internal/store"
)

type pushFile struct {
	Path   string `json:"path"`
	Sha256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Source string `json:"source"`
}

type pushVersion struct {
	N        int        `json:"n"`
	Commit   string     `json:"commit"`
	Bundle   string     `json:"bundle"`
	Title    string     `json:"title"`
	Prompts  []string   `json:"prompts"`
	Model    string     `json:"model,omitempty"`
	SavedAt  string     `json:"savedAt"`
	Readme   string     `json:"readme"`
	Files    []pushFile `json:"files"`
	Changes  []change   `json:"changes"`
	Assemble string     `json:"assemble"`
	Render   string     `json:"render,omitempty"`
	Poster   string     `json:"poster,omitempty"`
	Preview  string     `json:"preview,omitempty"`
	Duration float64    `json:"duration,omitempty"`
	Width    int        `json:"width,omitempty"`
	Height   int        `json:"height,omitempty"`
	FPS      float64    `json:"fps,omitempty"`
}

// blobSource is where a blob's bytes are: a file on disk (store objects, posters) or git output (bundles).
type blobSource struct {
	path string
	data []byte
	size int64
}

// pushBuild gathers what a push sends.
type pushBuild struct {
	dir, objects, tmp, gitDir string
	blobs                     map[string]blobSource
	noCopy                    map[string]bool     // why a 1080p copy wasn't made, once said
	code                      map[string]pushFile // by git object, since most text files repeat across versions
}

func (b pushBuild) addFile(sum, path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	b.blobs[sum] = blobSource{path: path, size: fi.Size()}
	return nil
}

func (b pushBuild) addData(data []byte) string {
	sum := store.Sum(data)
	b.blobs[sum] = blobSource{data: data, size: int64(len(data))}
	return sum
}

// version builds v's payload: its saved details, media, code, git bundle and, when it has a
// render, the render's shape, poster and, for a big render, a 1080p copy.
func (b pushBuild) version(v, prev string) (pushVersion, error) {
	n, _ := strconv.Atoi(strings.TrimPrefix(v, "v"))
	info, err := commitAt(b.dir, v)
	if err != nil {
		return pushVersion{}, err
	}
	assets, err := savedAssetsAt(b.dir, v)
	if err != nil {
		return pushVersion{}, err
	}
	commit, err := gitx.Run(b.dir, "rev-parse", v+"^{commit}")
	if err != nil {
		return pushVersion{}, err
	}
	entries, err := lsTree(b.dir, v)
	if err != nil {
		return pushVersion{}, err
	}
	readme, err := readmeIn(b.dir, entries)
	if err != nil {
		return pushVersion{}, err
	}
	// The bundle packs v's commits, minus what the previous version's bundle carried.
	bundleArgs := []string{"bundle", "create", "-", v}
	if prev != "" {
		bundleArgs = append(bundleArgs, "^"+prev)
	}
	bundle, err := gitx.Run(b.dir, bundleArgs...)
	if err != nil {
		return pushVersion{}, err
	}
	pv := pushVersion{
		N: n, Commit: strings.TrimSpace(commit), Bundle: b.addData([]byte(bundle)), Title: info.Message,
		Prompts: info.Prompts, Model: info.Model, SavedAt: info.SavedAt, Readme: readme,
		Changes: info.Changes, Assemble: info.Assemble, Render: info.Render, Files: []pushFile{},
	}
	if pv.Prompts == nil {
		pv.Prompts = []string{}
	}
	if pv.Changes == nil {
		pv.Changes = []change{}
	}
	for path, a := range assets {
		if !store.Has(b.objects, a.Sha256) {
			return pushVersion{}, fmt.Errorf("%s: %s is missing from .v2/objects", v, path)
		}
		pv.Files = append(pv.Files, pushFile{path, a.Sha256, a.Size, a.Source})
		if err := b.addFile(a.Sha256, store.Path(b.objects, a.Sha256)); err != nil {
			return pushVersion{}, err
		}
	}
	if err := b.addCode(v, entries, assets, &pv); err != nil {
		return pushVersion{}, err
	}
	if r, ok := assets[info.Render]; ok {
		if err := b.addRender(r, &pv); err != nil {
			return pushVersion{}, err
		}
	}
	return pv, nil
}

// readmeIn is the README.md in a version's tree, or "" when it has none.
func readmeIn(dir string, entries []treeEntry) (string, error) {
	for _, e := range entries {
		if e.path == "README.md" {
			return gitx.Run(dir, "cat-file", "blob", e.object)
		}
	}
	return "", nil
}

// addRender adds the render's shape, its poster and, for a big render, a 1080p copy.
func (b pushBuild) addRender(r asset, pv *pushVersion) error {
	src := store.Path(b.objects, r.Sha256)
	p := media.Probe(src)
	pv.Duration, pv.Width, pv.Height, pv.FPS = p.Duration, p.Width, p.Height, p.FPS
	if jpg := media.Poster(src, p.Duration/2, 1280); jpg != nil {
		pv.Poster = store.Sum(jpg)
		path := filepath.Join(b.tmp, pv.Poster+".jpg")
		if err := os.WriteFile(path, jpg, 0o644); err != nil {
			return err
		}
		if err := b.addFile(pv.Poster, path); err != nil {
			return err
		}
	}
	// The site plays the original; only a big one also gets a 1080p copy for slower connections.
	if min(p.Width, p.Height) > 1080 || p.Duration > 0 && float64(r.Size)*8/p.Duration > 8e6 {
		sum, err := b.addPreview(src, r.Sha256)
		if err != nil {
			// Every version fails the same way on a machine that can't encode, so each reason shows once.
			if !b.noCopy[err.Error()] {
				b.noCopy[err.Error()] = true
				fmt.Fprintf(os.Stderr, "warning: no 1080p copy for slower connections, so the site plays the full-size render: %v\n", err)
			}
			return nil
		}
		pv.Preview = sum
	}
	return nil
}

// addPreview makes the render's 1080p copy, kept in .git so the next push reuses it, and returns its sha256.
func (b pushBuild) addPreview(src, sum string) (string, error) {
	mp4 := filepath.Join(b.gitDir, "v2-1080p", sum+".mp4")
	if err := media.Copy1080(src, mp4, len(b.noCopy) == 0); err != nil {
		return "", err
	}
	preview, err := store.Hash(mp4)
	if err != nil {
		return "", err
	}
	return preview, b.addFile(preview, mp4)
}

// maxCodeFiles keeps a repo with a committed vendor folder from breaking its video page, which reads
// every listed file. All the code is still in the version's bundle.
const maxCodeFiles = 1000

// addCode sends version v's git-tracked files as readable files, because the site can't open a bundle.
// .v2/ is v2's own bookkeeping.
func (b pushBuild) addCode(v string, entries []treeEntry, assets map[string]asset, pv *pushVersion) error {
	sent := 0
	for _, e := range entries {
		if !strings.HasPrefix(e.mode, "100") || strings.HasPrefix(e.path, ".v2/") {
			continue
		}
		if _, isAsset := assets[e.path]; isAsset {
			continue
		}
		if sent == maxCodeFiles {
			fmt.Printf("%s has over %d text files; the site lists the first %d\n", v, maxCodeFiles, maxCodeFiles)
			break
		}
		sent++
		f, seen := b.code[e.object]
		if !seen {
			data, err := gitx.Run(b.dir, "cat-file", "blob", e.object)
			if err != nil {
				return err
			}
			f = pushFile{Sha256: b.addData([]byte(data)), Size: int64(len(data)), Source: "code"}
			b.code[e.object] = f
		}
		f.Path = e.path
		pv.Files = append(pv.Files, f)
	}
	return nil
}
