package main

// Talking to v2.video: the signed-in login, calls to the site's HTTP API, and the GETs and PUTs of
// blob bytes, which end only when no bytes move for a while.

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultServer = "https://festive-scorpion-564.convex.cloud"
	defaultSite   = "https://v2.video"
	// Storage takes at most 10,000 parts per file, so 64 MiB parts cover a file up to 625 GiB.
	partSize = 64 << 20
	tries    = 3
	// How many hashes go to the site in one call, to stay inside the site's per-call read limit.
	blobBatch = 200
	// A part's upload or download ends once no bytes move for this long. Not a limit on the whole
	// part, which takes many minutes on a slow link.
	stallTimeout = 60 * time.Second
)

var (
	apiClient  = &http.Client{Timeout: time.Minute}
	errStalled = fmt.Errorf("no data moved for %v", stallTimeout)
	// errUnreachable is a call that never got an answer, so an upload it was finishing may still be resumed.
	errUnreachable = errors.New("can't reach v2.video")
)

type login struct {
	Server string `json:"server"`
	Site   string `json:"site"` // the server sends page paths; this goes in front
	Token  string `json:"token"`
}

func loginFile() (string, error) {
	dir, err := os.UserConfigDir()
	return filepath.Join(dir, "v2", "login.json"), err
}

func readLogin() (login, error) {
	var l login
	path, err := loginFile()
	if err != nil {
		return l, err
	}
	if err := readJSON(path, &l); err != nil {
		return l, err
	}
	if l.Token == "" {
		return l, errors.New("not signed in; run `v2 login` first")
	}
	return l, nil
}

// authed is args with this login's token added, which every signed-in call sends.
func (l login) authed(args map[string]any) map[string]any {
	args["token"] = l.Token
	return args
}

// call runs one site function and decodes its value into out (nil to ignore it).
func (l login) call(kind, fn string, args map[string]any, out any) error {
	return l.callCtx(context.Background(), kind, fn, args, out)
}

func (l login) callCtx(ctx context.Context, kind, fn string, args map[string]any, out any) error {
	versioned := map[string]any{"version": version} // the site refuses a version too old to talk to
	maps.Copy(versioned, args)
	body, err := json.Marshal(map[string]any{"path": fn, "args": versioned, "format": "json"})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.Server+"/api/"+kind, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := apiClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", errUnreachable, err)
	}
	defer func() { _ = res.Body.Close() }()
	var r struct {
		Status       string          `json:"status"`
		Value        json.RawMessage `json:"value"`
		ErrorMessage string          `json:"errorMessage"`
		ErrorData    json.RawMessage `json:"errorData"`
		Message      string          `json:"message"` // a request the site rejects before running fn
	}
	err = json.NewDecoder(res.Body).Decode(&r)
	var syntax *json.SyntaxError
	var wrongType *json.UnmarshalTypeError
	switch {
	case r.Status == "" && res.StatusCode >= 500: // a 5xx with no function result is infrastructure: a gateway's page or an overloaded server
		if r.Message != "" {
			return fmt.Errorf("%w: %s got HTTP %d: %s", errUnreachable, fn, res.StatusCode, r.Message)
		}
		return fmt.Errorf("%w: %s got HTTP %d", errUnreachable, fn, res.StatusCode)
	case err != nil && !errors.Is(err, io.EOF) && !errors.As(err, &syntax) && !errors.As(err, &wrongType): // the reply was cut off
		return fmt.Errorf("%w: %s: %w", errUnreachable, fn, err)
	case err != nil:
		return fmt.Errorf("v2.video sent a bad reply to %s (HTTP %d)", fn, res.StatusCode)
	}
	if r.Status != "success" {
		var msg string // the function's own error message is in errorData
		_ = json.Unmarshal(r.ErrorData, &msg)
		return errors.New(cmp.Or(msg, r.ErrorMessage, r.Message, fmt.Sprintf("v2.video refused %s (HTTP %d)", fn, res.StatusCode)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(r.Value, out)
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return strings.TrimRight(v, "/")
	}
	return fallback
}

type stallTimer struct{ *time.Timer }

func (t stallTimer) Write(b []byte) (int, error) {
	t.Reset(stallTimeout)
	return len(b), nil
}

// untilStall runs f with a context that ends once nothing is written to moved for stallTimeout, and
// reports an f that failed because of that as errStalled.
func untilStall(f func(ctx context.Context, moved io.Writer) error) error {
	ctx, cancel := context.WithCancelCause(context.Background())
	t := time.AfterFunc(stallTimeout, func() { cancel(errStalled) })
	err := f(ctx, stallTimer{t})
	t.Stop()
	cancel(nil)
	if err != nil && errors.Is(context.Cause(ctx), errStalled) {
		return errStalled
	}
	return err
}

// statusError is an answer other than 200 OK.
type statusError struct {
	code   int
	status string
}

func (e statusError) Error() string { return fmt.Sprintf("HTTP %d", e.code) }

// getAll writes the bodies of urls, one after another, to w. A failed request is a *url.Error, and
// an answer other than 200 OK a statusError.
func getAll(urls []string, w io.Writer) error {
	return untilStall(func(ctx context.Context, moved io.Writer) error {
		for _, u := range urls {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
			if err != nil {
				return err
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				return err
			}
			if res.StatusCode != http.StatusOK {
				_ = res.Body.Close()
				return statusError{res.StatusCode, res.Status}
			}
			_, err = io.Copy(io.MultiWriter(w, moved), res.Body)
			_ = res.Body.Close()
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// fetch downloads u whole. Like a blob download, it ends only when no bytes move for a while.
func fetch(u string) ([]byte, error) {
	var buf bytes.Buffer
	err := getAll([]string{u}, &buf)
	var failed *url.Error
	var status statusError
	switch {
	case errors.As(err, &failed):
		return nil, fmt.Errorf("%w: %w", errUnreachable, err)
	case errors.As(err, &status):
		return nil, fmt.Errorf("downloading %s: %s", u, status.status)
	}
	return buf.Bytes(), err
}

var errNoUpload = errors.New("v2.video no longer has this file's unfinished upload; run `v2 push` again to start it over")

// putPart PUTs one part to a signed URL and returns its ETag, or errNoUpload once storage has dropped the upload.
func putPart(u string, part *io.SectionReader, sent io.Writer) (string, error) {
	var etag string
	err := untilStall(func(ctx context.Context, moved io.Writer) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, io.TeeReader(io.NewSectionReader(part, 0, part.Size()), io.MultiWriter(sent, moved)))
		if err != nil {
			return err
		}
		req.ContentLength = part.Size()
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
		etag = res.Header.Get("ETag")
		switch {
		case res.StatusCode == http.StatusOK && etag != "":
			return nil
		case res.StatusCode == http.StatusNotFound: // storage drops an unfinished upload after a week
			return errNoUpload
		}
		return fmt.Errorf("HTTP %d", res.StatusCode)
	})
	if err != nil {
		return "", err
	}
	return etag, nil
}
