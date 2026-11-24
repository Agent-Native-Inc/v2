package main

// Clone's downloads: the blobs a clone needs, fetched from the site into .v2/objects.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/Agent-Native-Inc/v2/internal/store"
)

// A blob stored in parts and never hashed is keyed by this prefix; only its size can be checked.
// Kept at store.Path like any other download, so a resumed clone finds it, and dropped at the end.
const unhashed = "unhashed:"

type siteBlob struct {
	Sha256 string
	Size   whole
	URLs   []string `json:"urls"`
}

// need is a blob the clone wants. Without must, a blob the site has no copy of is skipped with a warning.
type need struct {
	sum, path string
	must      bool
}

func (c *cloner) fetchReplayed(plan clonePlan, needs []need) error {
	versions := plan.replay
	if plan.starting != nil {
		versions = append([]siteVersion{*plan.starting}, versions...)
	}
	for _, v := range versions {
		for _, f := range v.Files {
			needs = append(needs, need{sum: f.Sha256, path: f.Path})
		}
		if v.Render != nil {
			needs = append(needs, need{sum: v.Render.Sha256, path: fmt.Sprintf("V%d's video %s", v.N, v.Render.Path), must: true})
		}
	}
	return c.fetch("media", needs)
}

func (c *cloner) fetch(what string, needs []need) error {
	have := map[string]bool{} // each sum needed, and whether the store has it
	var todo []string
	for _, n := range needs {
		if _, seen := have[n.sum]; seen || n.sum == "" {
			continue
		}
		have[n.sum] = store.Has(c.objects, n.sum)
		if !have[n.sum] {
			todo = append(todo, n.sum)
		}
	}
	blobs := map[string]siteBlob{}
	for batch := range slices.Chunk(todo, blobBatch) {
		var found []siteBlob
		args := c.l.authed(map[string]any{"videoId": c.videoID, "hashes": batch})
		if err := c.l.call("query", "cli:blobUrls", args, &found); err != nil {
			return err
		}
		for _, b := range found {
			if stored, asked := have[b.Sha256]; asked && !stored {
				blobs[b.Sha256] = b
			}
		}
	}
	for _, n := range needs {
		if _, ok := blobs[n.sum]; ok || have[n.sum] {
			continue
		}
		if n.must {
			return fmt.Errorf("%s isn't on v2.video, so this project can't be cloned", n.path)
		}
		c.skipped[n.path] = true
	}
	sent := &store.Progress{}
	for _, b := range blobs {
		sent.Total += int64(b.Size)
	}
	if len(blobs) == 0 {
		return nil
	}
	fmt.Printf("%s: %d file(s), %.1f MB\n", what, len(blobs), float64(sent.Total)/1e6)
	for _, sum := range slices.Sorted(maps.Keys(blobs)) {
		if err := download(c.objects, blobs[sum], sent); err != nil {
			return fmt.Errorf("%w\nNothing is lost: run the same command again to continue", err)
		}
	}
	return nil
}

// download puts a blob in the store under its hash, and only after its bytes check out.
func download(objects string, b siteBlob, sent *store.Progress) error {
	return store.Place(store.Path(objects, b.Sha256), func(tmp string) error {
		var sum string
		var err error
		before := sent.Done
		for try := 1; try <= tries; try++ {
			sent.Done = before
			if sum, err = getParts(b.URLs, tmp, sent); err == nil {
				break
			}
		}
		if err != nil {
			return fmt.Errorf("download failed %d times: %w", tries, err)
		}
		if strings.HasPrefix(b.Sha256, unhashed) {
			if fi, err := os.Stat(tmp); err != nil || fi.Size() != int64(b.Size) {
				return fmt.Errorf("a download came back the wrong size (expected %d bytes), so it wasn't kept", b.Size)
			}
		} else if sum != b.Sha256 {
			return fmt.Errorf("a download is corrupted (it should hash to %s, got %s), so it wasn't kept", b.Sha256, sum)
		}
		return nil
	})
}

// getParts downloads a blob's parts, in order, into tmp and returns their sha256.
func getParts(urls []string, tmp string, sent *store.Progress) (string, error) {
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	err = getAll(urls, io.MultiWriter(f, h, sent))
	closeErr := f.Close()
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), closeErr
}
