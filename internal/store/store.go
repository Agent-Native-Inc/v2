// Package store keeps every binary once, in dir/ab/cdef…, named by its sha256.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Put stores the file at path and returns its sha256.
func Put(dir, path string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	// Clone first, hash the clone. If we hashed the original, an agent still
	// writing it could change the bytes between hashing and copying, and the
	// object would be named after bytes it doesn't contain.
	tmp := filepath.Join(dir, fmt.Sprintf(".tmp-%d", os.Getpid()))
	_ = os.Remove(tmp)
	defer func() { _ = os.Remove(tmp) }()
	if err := Clone(path, tmp); err != nil {
		return "", fmt.Errorf("clone %s: %w", path, err)
	}
	sum, err := Hash(tmp)
	if err != nil {
		return "", err
	}
	obj := Path(dir, sum)
	if err := os.MkdirAll(filepath.Dir(obj), 0o755); err != nil {
		return "", err
	}
	// Always replace: same name means same bytes, so this also heals an object
	// that got corrupted. Rename is atomic: a crash leaves the old object or the new one, never half.
	return sum, os.Rename(tmp, obj)
}

// Get writes object sum to dest, and errors instead of writing bytes that don't match sum.
func Get(dir, sum, dest string) error {
	return Place(dest, func(tmp string) error {
		if err := Clone(Path(dir, sum), tmp); err != nil {
			return fmt.Errorf("object %s: %w", sum, err)
		}
		got, err := Hash(tmp)
		if err == nil && got != sum {
			err = fmt.Errorf("object %s is corrupted (its contents hash to %s)", sum, got)
		}
		return err
	})
}

// Place writes dest through dest.v2tmp: fill makes the temp file, and only a fill that succeeds is
// renamed to dest, so dest never holds half a file.
func Place(dest string, fill func(tmp string) error) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp := dest + ".v2tmp"
	_ = os.Remove(tmp)
	defer func() { _ = os.Remove(tmp) }()
	if err := fill(tmp); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}

// Path is where the object with this hash lives in the store.
func Path(dir, sum string) string {
	return filepath.Join(dir, sum[:2], sum[2:])
}

// Sum returns the sha256 of b, in hex.
func Sum(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Hash returns the sha256 of the file at path.
func Hash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Show the copy message and progress only for files this big; small copies are instant anyway.
const announceSize = 100 << 20

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	var w io.Writer = out
	if fi.Size() >= announceSize {
		fmt.Fprintf(os.Stderr, "copying %s%s…\n", human(fi.Size()), copyReason)
		w = io.MultiWriter(out, &Progress{Total: fi.Size()})
	}
	if _, err := io.Copy(w, in); err != nil {
		return errors.Join(err, out.Close(), os.Remove(dst))
	}
	if err := out.Close(); err != nil {
		return errors.Join(err, os.Remove(dst))
	}
	return nil
}

// Progress counts the bytes written to it and prints a line at every 10% of Total. Plain lines,
// not a redrawn bar, so agent logs stay readable.
type Progress struct {
	Total, Done int64
	shown       int64
}

func (p *Progress) Write(b []byte) (int, error) {
	p.Add(int64(len(b)))
	return len(b), nil
}

// Add counts n more bytes done.
func (p *Progress) Add(n int64) {
	p.Done += n
	if p.Total <= 0 {
		return
	}
	if tens := p.Done * 10 / p.Total * 10; tens > p.shown {
		p.shown = tens
		fmt.Fprintf(os.Stderr, "  %d%%\n", tens)
	}
}

func human(n int64) string {
	if n >= 1<<30 {
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	}
	return fmt.Sprintf("%d MB", n>>20)
}

// Has reports whether object sum is in the store.
func Has(dir, sum string) bool {
	_, err := os.Stat(Path(dir, sum))
	return err == nil
}
