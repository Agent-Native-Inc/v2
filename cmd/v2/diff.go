package main

// v2 diff: the versions page, served on this machine only.

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Agent-Native-Inc/v2/internal/media"
	"github.com/Agent-Native-Inc/v2/internal/platform"
	"github.com/Agent-Native-Inc/v2/internal/store"
)

// The page, built from web/ by `npm run build` (run it before `go build`; see CONTRIBUTING.md).
//
//go:embed all:dist
var page embed.FS

type versionView struct {
	Version  string   `json:"version"`
	Message  string   `json:"message"`
	Prompts  []string `json:"prompts,omitempty"`
	Model    string   `json:"model,omitempty"`
	SavedAt  string   `json:"saved_at"`
	Changes  []change `json:"changes"`
	Duration float64  `json:"duration"`
	Aspect   float64  `json:"aspect"` // width / height as displayed, so the page can size the player
	Video    bool     `json:"video"`
	sum, ext string
}

// diffServer answers the page. A version never changes once saved, so what's read about one, and
// about each render, is kept for as long as the server runs.
type diffServer struct {
	dir, objects string
	views        *memo[versionView] // by version
	probes       *memo[media.Info]  // by render sha256, like posters and peaks
	posters      *memo[[]byte]
	peaks        *memo[[]byte]
	mu           sync.Mutex // one comment edit at a time
}

// runDiff serves the versions page on this machine only, and opens it in the browser.
func runDiff(dir string, port int, open bool) error {
	dir, err := findProject(dir)
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	project := filepath.Base(abs)
	s := &diffServer{dir: dir, objects: objectsDir(dir)}
	s.views = newMemo(s.loadView)
	s.probes = newMemo(func(sum string) (media.Info, error) { return media.Probe(store.Path(s.objects, sum)), nil })
	s.posters = newMemo(func(sum string) ([]byte, error) {
		p, _ := s.probes.get(sum)
		return media.Poster(store.Path(s.objects, sum), p.Duration/2, 160), nil
	})
	s.peaks = newMemo(func(sum string) ([]byte, error) { return media.Peaks(store.Path(s.objects, sum)), nil })

	mux := http.NewServeMux()
	dist, _ := fs.Sub(page, "dist")
	mux.Handle("GET /", http.FileServerFS(dist))
	mux.HandleFunc("GET /api/versions", func(w http.ResponseWriter, _ *http.Request) {
		views, err := s.loadViews()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"project": project, "versions": views})
	})
	mux.HandleFunc("GET /poster/{v}", s.poster)
	mux.HandleFunc("GET /peaks/{v}", s.sound)
	mux.HandleFunc("GET /video/{v}", s.video)
	mux.HandleFunc("GET /api/comments", s.listComments)
	mux.HandleFunc("POST /api/comments", s.addComment)
	mux.HandleFunc("POST /api/comments/{id}/{action}", s.editComment)
	mux.HandleFunc("GET /comments/{file}", s.drawing)

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return err
	}
	url := "http://" + listener.Addr().String() + "/"
	fmt.Printf("versions of %s at %s (Ctrl-C to stop)\n", project, url)
	if open {
		openBrowser(url)
	}
	server := &http.Server{Handler: localOnly(mux), ReadHeaderTimeout: time.Minute}
	stopped, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() { <-stopped.Done(); _ = server.Close() }()
	if err := server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// localOnly refuses what another website open in the browser could send: a request to a name that
// DNS rebinding pointed here, or a cross-site write. Any local port is allowed, because the page's
// dev server proxies with its own origin (web/vite.config.ts); a website can't send a local name or origin.
func localOnly(next http.Handler) http.Handler {
	local := func(host string) bool {
		name := (&url.URL{Host: host}).Hostname()
		return name == "127.0.0.1" || name == "localhost"
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin, site := r.Header.Get("Origin"), r.Header.Get("Sec-Fetch-Site")
		u, err := url.Parse(origin)
		crossSite := origin != "" && (err != nil || !local(u.Host)) || site != "" && site != "same-origin" && site != "none"
		if !local(r.Host) || r.Method != http.MethodGet && r.Method != http.MethodHead && crossSite {
			http.Error(w, "only the versions page can use this server", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *diffServer) loadViews() ([]versionView, error) {
	versions, err := listVersions(s.dir)
	if err != nil {
		return nil, err
	}
	var views []versionView
	for _, v := range versions {
		view, err := s.views.get(v)
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, nil
}

func (s *diffServer) loadView(v string) (versionView, error) {
	info, err := commitAt(s.dir, v)
	if err != nil {
		return versionView{}, err
	}
	view := versionView{Version: v, Message: info.Message, Prompts: info.Prompts, Model: info.Model, SavedAt: info.SavedAt, Changes: info.Changes}
	if info.Render == "" {
		return view, nil
	}
	assets, err := assetsAt(s.dir, v)
	if err != nil {
		return versionView{}, err
	}
	render, ok := assets[info.Render]
	if !ok || !store.Has(s.objects, render.Sha256) {
		return view, nil
	}
	p, _ := s.probes.get(render.Sha256)
	view.Video, view.Duration, view.Aspect = true, p.Duration, p.Aspect
	view.sum, view.ext = render.Sha256, filepath.Ext(info.Render)
	return view, nil
}

// findVideo loads only the version asked for, and answers the request itself when it has no video.
// Only a name shaped like v12 reaches git, so input is never read as an option.
func (s *diffServer) findVideo(w http.ResponseWriter, r *http.Request) (versionView, bool) {
	v := r.PathValue("v")
	if !versionTag.MatchString(v) {
		http.NotFound(w, r)
		return versionView{}, false
	}
	view, err := s.views.get(v)
	if err != nil {
		if versions, listErr := listVersions(s.dir); listErr == nil && !slices.Contains(versions, v) {
			http.NotFound(w, r)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return versionView{}, false
	}
	if !view.Video {
		http.NotFound(w, r)
	}
	return view, view.Video
}

func (s *diffServer) poster(w http.ResponseWriter, r *http.Request) {
	view, ok := s.findVideo(w, r)
	if !ok {
		return
	}
	jpg, _ := s.posters.get(view.sum)
	if jpg == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "max-age=31536000, immutable")
	_, _ = w.Write(jpg)
}

// sound is the render's sound, 100 loudness samples a second, for the voice and music lanes.
func (s *diffServer) sound(w http.ResponseWriter, r *http.Request) {
	view, ok := s.findVideo(w, r)
	if !ok {
		return
	}
	peaks, _ := s.peaks.get(view.sum)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "max-age=31536000, immutable")
	_, _ = w.Write(peaks)
}

func (s *diffServer) video(w http.ResponseWriter, r *http.Request) {
	view, ok := s.findVideo(w, r)
	if !ok {
		return
	}
	f, err := os.Open(store.Path(s.objects, view.sum))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Only as video or audio, sandboxed: a render saved as .html or .svg would otherwise run as a page here.
	t := mime.TypeByExtension(view.ext)
	if !strings.HasPrefix(t, "video/") && !strings.HasPrefix(t, "audio/") {
		t = "application/octet-stream"
	}
	w.Header().Set("Content-Type", t)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("Cache-Control", "max-age=31536000, immutable")
	http.ServeContent(w, r, "", info.ModTime(), f)
}

// memo runs f once per key and keeps what it returns, so requests for one key wait for the same run
// while other keys run beside it. An error isn't kept: the next request runs f again.
type memo[V any] struct {
	f    func(key string) (V, error)
	mu   sync.Mutex
	runs map[string]func() (V, error)
}

func newMemo[V any](f func(key string) (V, error)) *memo[V] {
	return &memo[V]{f: f, runs: map[string]func() (V, error){}}
}

func (m *memo[V]) get(key string) (V, error) {
	m.mu.Lock()
	run, ok := m.runs[key]
	if !ok {
		run = sync.OnceValues(func() (V, error) { return m.f(key) })
		m.runs[key] = run
	}
	m.mu.Unlock()
	v, err := run()
	if err != nil {
		m.mu.Lock()
		delete(m.runs, key)
		m.mu.Unlock()
	}
	return v, err
}

func openBrowser(url string) {
	_ = exec.Command(platform.Opener, url).Start() // best effort; the URL is printed either way
}
