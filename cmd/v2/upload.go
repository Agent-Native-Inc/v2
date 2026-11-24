package main

// Uploading a push's blobs to R2: multipart, resumable, and checked against the account's room first.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/Agent-Native-Inc/v2/internal/store"
)

// uploads remembers multipart uploads not yet completed, so a push that dies (network, Ctrl-C)
// resumes from its last finished part. Kept in .git/, never part of a version. With no path it
// remembers them only in memory.
type uploads struct {
	path string
	R2   map[string]*r2Upload `json:"r2"` // by sha256
}

type r2Upload struct {
	UploadID string   `json:"uploadId"`
	ETags    []string `json:"etags"` // of its first parts, in order
}

func loadUploads(gitDir string) (*uploads, error) {
	d := &uploads{path: filepath.Join(gitDir, "v2-uploads.json"), R2: map[string]*r2Upload{}}
	if err := readJSON(d.path, d); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *uploads) save() error {
	if d.path == "" {
		return nil
	}
	return writeJSON(d.path, d)
}

// uploader sends blobs, counting the bytes sent in sent and its unfinished uploads in done.
type uploader struct {
	l     login
	blobs map[string]blobSource
	sent  *store.Progress
	done  *uploads
}

// uploadMissing sends the blobs the site lacks, once the account has room for them.
func uploadMissing(l login, missing []string, blobs map[string]blobSource, gitDir string) error {
	var total int64
	for _, h := range missing {
		total += blobs[h].size
	}
	if err := checkRoom(l, total); err != nil {
		return err
	}
	if len(missing) > 0 {
		fmt.Printf("uploading %d file(s), %.1f MB\n", len(missing), float64(total)/1e6)
	}
	done, err := loadUploads(gitDir)
	if err != nil {
		return err
	}
	u := uploader{l: l, blobs: blobs, sent: &store.Progress{Total: total}, done: done}
	if err := u.small(missing); err != nil {
		return err
	}
	for _, h := range missing {
		if blobs[h].path == "" {
			continue
		}
		if err := u.blob(h); err != nil {
			return err
		}
	}
	return nil
}

// checkRoom refuses a push the account has no room for before anything goes up, in the site's words.
// A site too old to send the numbers is left to refuse it itself.
func checkRoom(l login, total int64) error {
	if total == 0 {
		return nil
	}
	var who whoami
	if err := l.call("query", "cli:whoami", l.authed(map[string]any{}), &who); err != nil {
		return err
	}
	if who.StoredBytes == nil || who.LimitBytes == nil || float64(total) <= *who.LimitBytes-*who.StoredBytes {
		return nil
	}
	gb := func(n float64) string { return fmt.Sprintf("%.1f GB", n/(1<<30)) }
	return fmt.Errorf("This account's limit is %s and it holds %s, so this push's %s doesn't fit. Nothing was uploaded", gb(*who.LimitBytes), gb(*who.StoredBytes), gb(float64(total)))
}

// small sends the in-memory blobs (code, bundles) together: each takes several round trips, so
// one at a time a project's code alone took 46 s. They are too small to need resuming.
func (u uploader) small(missing []string) error {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	slots := make(chan struct{}, 8)
	for _, h := range missing {
		if u.blobs[h].path != "" {
			continue
		}
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			one := uploader{l: u.l, blobs: u.blobs, sent: &store.Progress{}, done: &uploads{R2: map[string]*r2Upload{}}}
			err := one.blob(h)
			mu.Lock()
			defer mu.Unlock()
			if err != nil && firstErr == nil {
				firstErr = err
			}
			if err == nil {
				u.sent.Add(u.blobs[h].size)
			}
		}()
	}
	wg.Wait()
	return firstErr
}

// blob sends a blob to R2 in parts of partSize, skipping parts an earlier push already
// sent, then has the site join them and register the file under its sha256.
func (u uploader) blob(sum string) error {
	src := u.blobs[sum]
	var r io.ReaderAt = bytes.NewReader(src.data)
	if src.path != "" {
		f, err := os.Open(src.path)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		r = f
	}
	// Without a type, the video is served as application/octet-stream and Chrome blocks its range requests (ORB).
	ctype := mime.TypeByExtension(filepath.Ext(src.path))
	if ctype == "" {
		head := make([]byte, 512)
		k, _ := r.ReadAt(head, 0)
		ctype = http.DetectContentType(head[:k])
	}
	up := u.done.R2[sum]
	if up == nil {
		up = &r2Upload{ETags: []string{}}
		if err := u.l.call("action", "cli:r2Start", u.l.authed(map[string]any{"sha256": sum, "size": src.size, "contentType": ctype}), up); err != nil {
			return err
		}
		u.done.R2[sum] = up
		if err := u.done.save(); err != nil {
			return err
		}
	}
	forget := func() error {
		delete(u.done.R2, sum)
		return u.done.save()
	}
	u.sent.Add(min(int64(len(up.ETags))*partSize, src.size))
	for off := int64(len(up.ETags)) * partSize; off < src.size || len(up.ETags) == 0; off += partSize {
		n := min(partSize, src.size-off)
		etag, err := u.part(sum, up.UploadID, len(up.ETags)+1, io.NewSectionReader(r, off, n))
		if errors.Is(err, errNoUpload) {
			err = errors.Join(err, forget())
		}
		if err != nil {
			return err
		}
		up.ETags = append(up.ETags, etag)
		if err := u.done.save(); err != nil {
			return err
		}
	}
	err := u.l.call("action", "cli:r2Complete", u.l.authed(map[string]any{"sha256": sum, "size": src.size, "uploadId": up.UploadID, "etags": up.ETags}), nil)
	if !errors.Is(err, errUnreachable) {
		err = errors.Join(err, forget()) // a refused upload can't be reused either; the next push starts this file over
	}
	return err
}

// part PUTs one part to a fresh signed URL, retrying a failed attempt, and returns its ETag.
func (u uploader) part(sum, uploadID string, number int, part *io.SectionReader) (string, error) {
	var lastErr error
	for try := 1; try <= tries; try++ {
		var signed struct{ URLs []string }
		if err := u.l.call("action", "cli:r2Parts", u.l.authed(map[string]any{"sha256": sum, "uploadId": uploadID, "parts": []int{number}}), &signed); err != nil {
			return "", err
		}
		if len(signed.URLs) != 1 {
			return "", fmt.Errorf("v2.video sent %d upload links for one part", len(signed.URLs))
		}
		before := u.sent.Done
		etag, err := putPart(signed.URLs[0], part, u.sent)
		if err == nil || errors.Is(err, errNoUpload) {
			return etag, err
		}
		lastErr = err
		u.sent.Done = before
	}
	return "", fmt.Errorf("upload failed %d times (%w); run `v2 push` again to continue where it stopped", tries, lastErr)
}
