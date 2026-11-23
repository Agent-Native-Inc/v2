package main

import (
	"context"
	"embed"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Agent-Native-Inc/v2-cli/internal/platform"
	"github.com/Agent-Native-Inc/v2-cli/internal/store"
)

// The page, built from web/ by `npm run build` (run it before `go build`; see HANDOFF.md).
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
	videos := &videoCache{objects: objectsDir(dir), probes: map[string]probed{}, posters: map[string][]byte{}, sounds: map[string][]byte{}}

	mux := http.NewServeMux()
	dist, _ := fs.Sub(page, "dist")
	mux.Handle("GET /", http.FileServerFS(dist))
	mux.HandleFunc("GET /api/versions", func(w http.ResponseWriter, _ *http.Request) {
		views, err := loadViews(dir, videos)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"project": project, "versions": views})
	})
	mux.HandleFunc("GET /poster/{v}", func(w http.ResponseWriter, r *http.Request) {
		view, ok := findVideo(w, r, dir, videos)
		if !ok {
			return
		}
		jpg := videos.poster(view.sum, view.Duration/2)
		if jpg == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "max-age=31536000, immutable")
		_, _ = w.Write(jpg)
	})
	// The render's sound, 100 loudness samples a second, for the voice and music lanes.
	mux.HandleFunc("GET /peaks/{v}", func(w http.ResponseWriter, r *http.Request) {
		view, ok := findVideo(w, r, dir, videos)
		if !ok {
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Cache-Control", "max-age=31536000, immutable")
		_, _ = w.Write(videos.peaks(view.sum))
	})
	mux.HandleFunc("GET /video/{v}", func(w http.ResponseWriter, r *http.Request) {
		view, ok := findVideo(w, r, dir, videos)
		if !ok {
			return
		}
		f, err := os.Open(store.Path(videos.objects, view.sum))
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
		if t := mime.TypeByExtension(view.ext); t != "" {
			w.Header().Set("Content-Type", t)
		}
		w.Header().Set("Cache-Control", "max-age=31536000, immutable")
		http.ServeContent(w, r, "", info.ModTime(), f)
	})

	commentRoutes(mux, dir)

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
	return server.Serve(listener)
}

// localOnly refuses what another website open in the browser could send: a request to a name that
// DNS rebinding pointed here, or a cross-site write. Any local port is allowed, because the page's
// dev server proxies with its own origin (CLAUDE.md); a website can't send a local name or origin.
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

func loadViews(dir string, videos *videoCache) ([]versionView, error) {
	versions, err := listVersions(dir)
	if err != nil {
		return nil, err
	}
	var views []versionView
	for _, v := range versions {
		view, err := loadView(dir, videos, v)
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, nil
}

func loadView(dir string, videos *videoCache, v string) (versionView, error) {
	info, err := commitAt(dir, v)
	if err != nil {
		return versionView{}, err
	}
	view := versionView{Version: v, Message: info.Message, Prompts: info.Prompts, Model: info.Model, SavedAt: info.SavedAt, Changes: info.Changes}
	if info.Render == "" {
		return view, nil
	}
	assets, err := assetsAt(dir, v)
	if err != nil {
		return versionView{}, err
	}
	render, ok := assets[info.Render]
	if !ok || !store.Has(videos.objects, render.Sha256) {
		return view, nil
	}
	p := videos.probe(render.Sha256)
	view.Video, view.Duration, view.Aspect = true, p.duration, p.aspect
	view.sum, view.ext = render.Sha256, filepath.Ext(info.Render)
	return view, nil
}

// findVideo loads only the version asked for, and answers the request itself when it has no video.
// Only a name from the version list reaches git, so input is never read as an option.
func findVideo(w http.ResponseWriter, r *http.Request, dir string, videos *videoCache) (versionView, bool) {
	versions, err := listVersions(dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return versionView{}, false
	}
	if !slices.Contains(versions, r.PathValue("v")) {
		http.NotFound(w, r)
		return versionView{}, false
	}
	view, err := loadView(dir, videos, r.PathValue("v"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return versionView{}, false
	}
	if !view.Video {
		http.NotFound(w, r)
	}
	return view, view.Video
}

type videoCache struct {
	objects string
	mu      sync.Mutex
	probes  map[string]probed
	posters map[string][]byte
	sounds  map[string][]byte
}

func (c *videoCache) probe(sum string) probed {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.probes[sum]
	if !ok {
		p = probe(store.Path(c.objects, sum))
		c.probes[sum] = p
	}
	return p
}

func (c *videoCache) poster(sum string, at float64) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	jpg, ok := c.posters[sum]
	if !ok {
		jpg = poster(store.Path(c.objects, sum), at, 160)
		c.posters[sum] = jpg
	}
	return jpg
}

func (c *videoCache) peaks(sum string) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.sounds[sum]
	if !ok {
		b = peaks(store.Path(c.objects, sum))
		c.sounds[sum] = b
	}
	return b
}

// peaks is the sound's loudness, 100 values a second (the loudest sample in each 10 ms), as
// little-endian float32s: about 24 KB a minute. Empty if there's no sound.
func peaks(path string) []byte {
	pcm, _ := exec.Command("ffmpeg", "-v", "error", "-i", path, "-vn", "-ac", "1", "-ar", "8000", "-f", "f32le", "-").Output()
	const per = 80 * 4 // 10 ms of 8 kHz float32 samples
	out := make([]byte, 0, len(pcm)/per*4+4)
	for i := 0; i+4 <= len(pcm); i += per {
		var peak float32
		for j := i; j+4 <= min(i+per, len(pcm)); j += 4 {
			peak = max(peak, float32(math.Abs(float64(math.Float32frombits(binary.LittleEndian.Uint32(pcm[j:]))))))
		}
		out = binary.LittleEndian.AppendUint32(out, math.Float32bits(peak))
	}
	return out
}

type probed struct {
	duration, aspect, fps float64
	width, height         int // as shown, after rotation
}

// probe asks ffprobe how long the video is and its shape as shown (a phone video stored
// landscape with a 90° rotation is portrait). Zeros if the file confuses it; the page then
// reads both from the video itself.
func probe(path string) probed {
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries",
		"format=duration:stream=width,height,avg_frame_rate:stream_side_data_list", "-of", "json", path).Output()
	if err != nil {
		return probed{}
	}
	var r struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			Width     int    `json:"width"`
			Height    int    `json:"height"`
			FrameRate string `json:"avg_frame_rate"`
			SideData  []struct {
				Rotation float64 `json:"rotation"`
			} `json:"side_data_list"`
		} `json:"streams"`
	}
	if json.Unmarshal(out, &r) != nil {
		return probed{}
	}
	var p probed
	p.duration, _ = strconv.ParseFloat(r.Format.Duration, 64)
	if len(r.Streams) > 0 && r.Streams[0].Height > 0 {
		w, h := float64(r.Streams[0].Width), float64(r.Streams[0].Height)
		for _, sd := range r.Streams[0].SideData {
			if int(math.Abs(sd.Rotation))%180 == 90 {
				w, h = h, w
			}
		}
		p.aspect, p.width, p.height = w/h, int(w), int(h)
		if num, den, ok := strings.Cut(r.Streams[0].FrameRate, "/"); ok {
			a, _ := strconv.ParseFloat(num, 64)
			b, _ := strconv.ParseFloat(den, 64)
			if b > 0 {
				p.fps = a / b
			}
		}
	}
	return p
}

// poster is one frame of the render, width pixels wide; nil if ffmpeg can't.
func poster(path string, at float64, width int) []byte {
	out, err := exec.Command("ffmpeg", "-v", "error", "-ss", strconv.FormatFloat(at, 'f', 2, 64), "-i", path,
		"-frames:v", "1", "-vf", "scale=min("+strconv.Itoa(width)+"\\,iw):-2", "-f", "image2", "-c:v", "mjpeg", "-").Output()
	if err != nil || len(out) == 0 {
		return nil
	}
	return out
}

func openBrowser(url string) {
	_ = exec.Command(platform.Opener, url).Start() // best effort; the URL is printed either way
}
