// Package walk lists a project's files, skips junk, splits text from binary,
// and remembers binary hashes so unchanged files are never read twice.
package walk

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Agent-Native-Inc/v2-cli/internal/gitx"
	"github.com/Agent-Native-Inc/v2-cli/internal/store"
	"golang.org/x/sys/unix"
)

// File is one file in the project.
type File struct {
	Path   string // relative to the project root, forward slashes
	Size   int64
	Binary bool
	Sha256 string // binaries only
	Link   bool   // a symlink: git versions the link, v2 never follows it
}

// Media and other formats that are always binary. Anything else is sniffed.
var binaryExt = map[string]bool{}

func init() {
	for e := range strings.FieldsSeq(`mp4 mov m4v mkv webm avi mxf mts
		mp3 wav aac m4a flac ogg aif aiff
		png jpg jpeg gif webp heic tif tiff exr psd bmp svg cube
		blend fbx glb obj aep zip gz tar ttf otf woff woff2 pdf`) {
		binaryExt["."+e] = true
	}
}

// key is what must stay equal for a cached hash to be trusted. inode and ctime catch a
// file replaced with same size and a copied-back mtime (cp -p, rsync -t, unzip).
type key struct {
	Size  int64  `json:"Size"`
	Mtime int64  `json:"Mtime"`
	Ctime int64  `json:"Ctime"`
	Inode uint64 `json:"Inode"`
}

const hashCache = ".v2/hashcache"

// clockFile is in .v2/, not .v2/objects/, which can be a link to another disk. v2's .gitignore lists it.
const clockFile = ".v2/clock"

type entry struct {
	key

	Binary bool   `json:"Binary"`
	Sha256 string `json:"Sha256,omitempty"`
	// Racy: the file's mtime was not older than the disk's clock when the entry was made, so a write
	// after that could leave the same key (FAT steps mtime by 2 s and keeps the inode). The next scan
	// reads the file instead of trusting the key, once the disk's clock has reached the file's mtime.
	// This is git's "racily clean" rule.
	Racy bool `json:"Racy,omitempty"`
}

// Scan walks root. hash is called only for binaries whose cache entry is missing or stale;
// save passes a function that also stores the file, restore passes a plain hash.
func Scan(root string, hash func(path string) (string, error)) ([]File, error) {
	skip, err := ignored(root)
	if err != nil {
		return nil, err
	}
	cachePath := filepath.Join(root, hashCache)
	cache := readCache(cachePath)
	now, err := diskClock(root)
	if err != nil {
		return nil, err
	}
	seen := map[string]entry{}

	var files []File
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		switch {
		case rel == ".":
			return nil
		case d.IsDir() && (d.Name() == ".git" || rel == ".v2" || skip[rel] || AppleDouble(rel)):
			return filepath.SkipDir
		case d.IsDir() || skip[rel] || AppleDouble(rel):
			return nil
		case d.Type()&fs.ModeSymlink != 0:
			files = append(files, File{Path: rel, Link: true})
			return nil
		case !d.Type().IsRegular():
			return nil
		}

		e, err := scanFile(p, cache[rel], now, hash)
		if err != nil {
			return err
		}
		seen[rel] = e
		files = append(files, File{Path: rel, Size: e.Size, Binary: e.Binary, Sha256: e.Sha256})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, writeCache(cachePath, seen)
}

// AppleDouble reports whether rel is one of the Mac's ._ files, or inside a folder named like one. On a
// drive that can't hold extended attributes (exFAT, FAT, most network shares) the Mac keeps them in
// ._name next to name, and moves or deletes that file itself whenever name is renamed or replaced.
// No version holds them; git skips them through the same pattern in .gitignore.
func AppleDouble(rel string) bool {
	return strings.HasPrefix(rel, "._") || strings.Contains(rel, "/._")
}

// Remember records files v2 just placed and checked against their sha256 (restore, clone), so the next
// scan trusts the binaries among them instead of hashing them through the store, which copies them on
// a disk without clones. A file that's already gone is skipped: on exFAT the Mac deletes a ._ file
// when its partner is replaced.
func Remember(root string, shas map[string]string) error {
	cachePath := filepath.Join(root, hashCache)
	cache := readCache(cachePath)
	now, err := diskClock(root)
	if err != nil {
		return err
	}
	for rel, sum := range shas {
		p := filepath.Join(root, rel)
		k, err := statKey(p)
		if err != nil {
			continue
		}
		if bin, err := IsBinary(p); err == nil && bin {
			cache[rel] = entry{key: k, Binary: true, Sha256: sum, Racy: k.Mtime >= now}
		}
	}
	return writeCache(cachePath, cache)
}

func statKey(p string) (key, error) {
	var st unix.Stat_t
	if err := unix.Lstat(p, &st); err != nil {
		return key{}, &os.PathError{Op: "lstat", Path: p, Err: err}
	}
	return key{st.Size, st.Mtim.Nano(), st.Ctim.Nano(), st.Ino}, nil
}

// diskClock is the mtime the project's disk gives a file made now, so it has the disk's precision and,
// on a network disk, the server's clock. Read it before the files' own times.
func diskClock(root string) (int64, error) {
	p := filepath.Join(root, clockFile)
	f, err := os.Create(p)
	if err != nil {
		return 0, err
	}
	defer func() {
		_ = f.Close()
		_ = os.Remove(p)
	}()
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	return st.ModTime().UnixNano(), nil
}

func scanFile(p string, cached entry, now int64, hash func(string) (string, error)) (entry, error) {
	k, err := statKey(p)
	if err != nil {
		return entry{}, err
	}
	racy := k.Mtime >= now
	if cached.key == k && (!cached.Binary || cached.Sha256 != "") {
		// A key dated after the disk's clock can't come from a write made before now (unless a tool
		// copied the file's date: cp -p, rsync -t, unzip), so it still holds.
		if !cached.Racy || cached.Mtime > now {
			return cached, nil
		}
		// Read it in place first: hash would copy an unchanged video into the store again on a disk without clones.
		if cached.Binary {
			sum, err := store.Hash(p)
			if err != nil {
				return entry{}, err
			}
			if sum == cached.Sha256 {
				cached.Racy = racy
				return cached, nil
			}
		}
	}

	e := entry{key: k, Racy: racy}
	if e.Binary, err = IsBinary(p); err != nil {
		return entry{}, err
	}
	if e.Binary {
		e.Sha256, err = hash(p)
	}
	return e, err
}

func readCache(path string) map[string]entry {
	b, _ := os.ReadFile(path)
	cache := map[string]entry{}
	if json.Unmarshal(b, &cache) != nil {
		return map[string]entry{} // a missing or broken cache just means re-hashing
	}
	return cache
}

func writeCache(path string, cache map[string]entry) error {
	b, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// IsBinary reports whether path is binary: a known extension, else a NUL byte in the first 8 KB (git's rule).
func IsBinary(path string) (bool, error) {
	if binaryExt[strings.ToLower(filepath.Ext(path))] {
		return true, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, 8000)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return false, err
	}
	return bytes.IndexByte(buf[:n], 0) >= 0, nil
}

// Ignore returns the patterns in root/.v2ignore.
// They use .gitignore syntax: git does the matching (see ignored), and save copies them for git to skip too.
func Ignore(root string) ([]string, error) {
	b, err := os.ReadFile(filepath.Join(root, ".v2ignore"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var patterns []string
	for line := range strings.SplitSeq(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			patterns = append(patterns, line)
		}
	}
	return patterns, nil
}

// ignored asks git which paths .v2ignore covers, so v2 and git read it exactly alike
// (**, ! and anchors included). Ignored folders come back whole, so node_modules isn't listed file by file.
func ignored(root string) (map[string]bool, error) {
	skip := map[string]bool{}
	if fi, _ := os.Stat(filepath.Join(root, ".v2ignore")); fi == nil {
		return skip, nil
	}
	untracked, err := gitx.IgnoredOthers(root, "--exclude-from=.v2ignore")
	if err != nil {
		return nil, err
	}
	out, err := gitx.Run(root, "ls-files", "-z", "--ignored", "--exclude-from=.v2ignore", "--cached")
	if err != nil {
		return nil, err
	}
	for _, p := range append(untracked, strings.Split(out, "\x00")...) {
		if p != "" {
			skip[strings.TrimSuffix(p, "/")] = true
		}
	}
	return skip, nil
}
