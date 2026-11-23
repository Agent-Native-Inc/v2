package main

// v2 login / logout / push: send saved versions to v2.video. Talks to the site's Convex
// functions (convex/cli.ts) over Convex's HTTP API. Media goes up once per user, keyed by sha256.

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Agent-Native-Inc/v2-cli/internal/gitx"
	"github.com/Agent-Native-Inc/v2-cli/internal/store"
)

const (
	defaultServer = "https://festive-scorpion-564.convex.cloud"
	defaultSite   = "https://v2.video"
	// R2 takes at most 10,000 parts per file, so 64 MiB parts cover a file up to 625 GiB.
	partSize = 64 << 20
	tries    = 3
	// How many hashes go to the site in one call, to stay inside Convex's read limit.
	blobBatch = 200
	// A part's upload or download ends once no bytes move for this long. Not a limit on the whole
	// part, which takes many minutes on a slow link.
	stallTimeout = 60 * time.Second
)

var (
	apiClient  = &http.Client{Timeout: time.Minute}
	errStalled = fmt.Errorf("no data moved for %v", stallTimeout)
)

type stallTimer struct{ *time.Timer }

func (t stallTimer) Write(b []byte) (int, error) {
	t.Reset(stallTimeout)
	return len(b), nil
}

// watchStall returns a context that ends once nothing is written to moved for stallTimeout, and
// settle, which stops the watch and reports a stall as errStalled.
func watchStall() (context.Context, io.Writer, func(error) error) {
	ctx, cancel := context.WithCancelCause(context.Background())
	t := time.AfterFunc(stallTimeout, func() { cancel(errStalled) })
	return ctx, stallTimer{t}, func(err error) error {
		t.Stop()
		cancel(nil)
		if err != nil && errors.Is(context.Cause(ctx), errStalled) {
			return errStalled
		}
		return err
	}
}

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

// call runs a Convex query or mutation and decodes its value into out (nil to ignore it).
// errUnreachable is a call that never got an answer, so an upload it was finishing may still be resumed.
var errUnreachable = errors.New("can't reach v2.video")

func (l login) call(kind, fn string, args map[string]any, out any) error {
	versioned := map[string]any{"version": version} // the site refuses a version too old to talk to
	maps.Copy(versioned, args)
	body, err := json.Marshal(map[string]any{"path": fn, "args": versioned, "format": "json"})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, l.Server+"/api/"+kind, bytes.NewReader(body))
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
		Message      string          `json:"message"` // a request Convex rejects before running fn
	}
	err = json.NewDecoder(res.Body).Decode(&r)
	var syntax *json.SyntaxError
	var wrongType *json.UnmarshalTypeError
	switch {
	case r.Status == "" && res.StatusCode >= 500: // a 5xx with no function result is infrastructure: a gateway's page or an overloaded Convex
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
		var msg string // a ConvexError's own message is in errorData
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

func runLogin() error {
	server := envOr("V2_SERVER", defaultServer)
	// Agents run login before every push, so a login that still works ends here instead of printing a code and waiting for an approval.
	if old, err := readLogin(); err == nil && old.Server == server {
		var who *struct{ Name, Email string }
		if old.call("query", "cli:whoami", map[string]any{"token": old.Token}, &who) == nil && who != nil {
			fmt.Printf("Already signed in as %s (%s). To switch accounts, run `v2 logout` first.\n", who.Name, who.Email)
			return nil
		}
	}
	b := make([]byte, 24)
	rand.Read(b)
	l := login{Server: server, Site: envOr("V2_SITE", defaultSite), Token: "v2t_" + hex.EncodeToString(b)}
	sum := sha256.Sum256([]byte(l.Token))
	var start struct{ Code, URL string }
	if err := l.call("mutation", "cli:startLogin", map[string]any{"tokenHash": hex.EncodeToString(sum[:])}, &start); err != nil {
		return err
	}
	fmt.Printf("Open %s and approve code %s there\n", l.Site+start.URL, start.Code)
	openBrowser(l.Site + start.URL)
	var down error // the last check, when it got no answer: the approval can still land, so login keeps waiting
	for deadline := time.Now().Add(10 * time.Minute); time.Now().Before(deadline); time.Sleep(2 * time.Second) {
		var who *struct{ Name, Email string }
		err := l.call("query", "cli:whoami", map[string]any{"token": l.Token}, &who)
		if errors.Is(err, errUnreachable) {
			if down == nil {
				fmt.Fprintf(os.Stderr, "%v; still waiting for the approval\n", err)
			}
			down = err
			continue
		}
		down = nil
		if err != nil {
			return err
		}
		if who == nil {
			continue
		}
		path, err := loginFile()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		j, err := json.Marshal(l)
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, j, 0o600); err != nil {
			return err
		}
		fmt.Printf("Signed in as %s (%s)\n", who.Name, who.Email)
		return nil
	}
	if down != nil {
		return fmt.Errorf("%w; the code expired after 10 minutes, run `v2 login` again", down)
	}
	return errors.New("code expired after 10 minutes; run `v2 login` again")
}

func runLogout() error {
	l, err := readLogin()
	if err != nil {
		return err
	}
	path, _ := loginFile()
	if err := os.Remove(path); err != nil {
		return err
	}
	if err := l.call("mutation", "cli:logout", map[string]any{"token": l.Token}, nil); err != nil {
		return fmt.Errorf("signed out on this computer, but v2.video couldn't be told: %w", err)
	}
	return nil
}

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
}

var heading = regexp.MustCompile(`(?m)^#\s+(.+)$`)

func runPush(dir string) error {
	dir, err := findProject(dir)
	if err != nil {
		return err
	}
	l, err := readLogin()
	if err != nil {
		return err
	}
	versions, err := listVersions(dir)
	if err != nil {
		return err
	}
	if len(versions) == 0 {
		return errors.New("nothing to push; save a version first")
	}
	tmp, err := os.MkdirTemp("", "v2push")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	gitDir, err := gitx.Run(dir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return err
	}
	gitDir = strings.TrimSpace(gitDir)

	blobs := map[string]blobSource{}
	b := pushBuild{dir: dir, tmp: tmp, gitDir: gitDir, blobs: blobs, code: map[string]pushFile{}, noCopy: map[string]bool{}}
	var payload []pushVersion
	prev := ""
	// ponytail: rebuilds every version's bundle and poster on each push; skip versions the site has if pushes get slow.
	for _, v := range versions {
		pv, err := b.version(v, prev)
		if err != nil {
			return err
		}
		payload = append(payload, pv)
		prev = v
	}

	hashes := make([]string, 0, len(blobs))
	for h := range blobs {
		hashes = append(hashes, h)
	}
	videoID, _ := gitx.Run(dir, "config", "--get", "v2.video")
	videoID = strings.TrimSpace(videoID)
	forkOf, _ := gitx.Run(dir, "config", "--get", "v2.forkOf")
	forkOf = strings.TrimSpace(forkOf)
	missing, err := missingBlobs(l, hashes, videoID == "" && forkOf != "", forkOf)
	if err != nil {
		return err
	}
	args := map[string]any{"token": l.Token, "versions": payload}
	if videoID != "" {
		args["videoId"] = videoID
	} else {
		args["title"] = projectTitle(dir, payload[len(payload)-1].Readme)
		if forkOf != "" {
			n, _ := gitx.Run(dir, "config", "--get", "v2.forkN")
			forkN, _ := strconv.Atoi(strings.TrimSpace(n))
			args["forkOf"] = map[string]any{"videoId": forkOf, "n": forkN}
		}
	}
	// The site names a new project before it looks at any file, so this push tells a taken name
	// before anything goes up. With the name free, the site refuses the files it lacks and keeps nothing.
	// ponytail: knows the refusal by its first words; a reworded one arrives after the upload again. A name query on the site would end that.
	if videoID == "" && forkOf == "" && len(missing) > 0 {
		if err := l.call("mutation", "cli:push", args, nil); err != nil && strings.HasPrefix(err.Error(), "You already have a project called") {
			return err
		}
	}
	if err := uploadMissing(l, missing, blobs, gitDir, tmp); err != nil {
		return err
	}

	var out struct {
		VideoID string    `json:"videoId"`
		URL     string    `json:"url"`
		Pushed  []float64 `json:"pushed"` // Convex sends every number as a float: [1.0, 2.0]
	}
	if err := l.call("mutation", "cli:push", args, &out); err != nil {
		return err
	}
	// Kept in .git/config: never part of a version, so restoring one can't lose it.
	if _, err := gitx.Run(dir, "config", "v2.video", out.VideoID); err != nil {
		return err
	}
	if err := sendSiteResolves(dir, l, out.VideoID); err != nil {
		return fmt.Errorf("pushed, but the fixed comments are still open on the site, the next push retries them: %w", err)
	}
	if len(out.Pushed) == 0 {
		fmt.Printf("already up to date: %s\n", l.Site+out.URL)
		return nil
	}
	fmt.Printf("pushed v%g-v%g: %s\n", out.Pushed[0], out.Pushed[len(out.Pushed)-1], l.Site+out.URL)
	return nil
}

// missingBlobs returns the hashes the site doesn't have, after sharing a fork's blobs from the
// project forkOf when share is set.
func missingBlobs(l login, hashes []string, share bool, forkOf string) ([]string, error) {
	if share {
		for batch := range slices.Chunk(hashes, blobBatch) {
			if err := l.call("mutation", "cli:shareBlobs", map[string]any{"token": l.Token, "from": forkOf, "hashes": batch}, nil); err != nil {
				return nil, err
			}
		}
	}
	var missing []string
	for batch := range slices.Chunk(hashes, blobBatch) {
		var some []string
		if err := l.call("query", "cli:missingBlobs", map[string]any{"token": l.Token, "hashes": batch}, &some); err != nil {
			return nil, err
		}
		missing = append(missing, some...)
	}
	return missing, nil
}

// uploadMissing sends the blobs the site lacks, once the account has room for them.
func uploadMissing(l login, missing []string, blobs map[string]blobSource, gitDir, tmp string) error {
	var total int64
	for _, h := range missing {
		total += blobs[h].size()
	}
	if err := checkRoom(l, total); err != nil {
		return err
	}
	if len(missing) > 0 {
		fmt.Printf("uploading %d file(s), %.1f MB\n", len(missing), float64(total)/1e6)
	}
	sent := &store.Progress{Total: total}
	done, err := loadUploads(gitDir)
	if err != nil {
		return err
	}
	if err := uploadSmall(l, missing, blobs, sent, tmp); err != nil {
		return err
	}
	for _, h := range missing {
		if blobs[h].path == "" {
			continue
		}
		if err := uploadBlob(l, h, blobs[h], sent, done); err != nil {
			return err
		}
	}
	return nil
}

// checkRoom refuses a push the account has no room for before anything goes up, in the site's words.
// A site too old to send the numbers is left to refuse it itself.
func checkRoom(l login, total int64) error {
	var who struct{ StoredBytes, LimitBytes *float64 }
	if err := l.call("query", "cli:whoami", map[string]any{"token": l.Token}, &who); err != nil {
		return err
	}
	if who.StoredBytes == nil || who.LimitBytes == nil || total == 0 || float64(total) <= *who.LimitBytes-*who.StoredBytes {
		return nil
	}
	gb := func(n float64) string { return fmt.Sprintf("%.1f GB", n/(1<<30)) }
	return fmt.Errorf("This account's limit is %s and it holds %s, so this push's %s doesn't fit. Nothing was uploaded", gb(*who.LimitBytes), gb(*who.StoredBytes), gb(float64(total)))
}

// pushBuild gathers what a push sends: each version's payload, and every blob the payloads name.
type pushBuild struct {
	dir, tmp, gitDir string
	blobs            map[string]blobSource
	noCopy           map[string]bool     // why a 1080p copy wasn't made, once said
	code             map[string]pushFile // by git object, since most text files repeat across versions
}

// version builds v's payload: its saved details, media, code, git bundle and, when it has a
// render, the render's shape, poster and, for a big render, a 1080p copy.
func (b pushBuild) version(v, prev string) (pushVersion, error) {
	objects := objectsDir(b.dir)
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
	readme, _ := gitx.Run(b.dir, "show", v+":README.md") // a project without one sends ""
	// The bundle packs v's commits, minus what the previous version's bundle carried.
	bundleArgs := []string{"bundle", "create", "-", v}
	if prev != "" {
		bundleArgs = append(bundleArgs, "^"+prev)
	}
	bundle, err := gitx.Run(b.dir, bundleArgs...)
	if err != nil {
		return pushVersion{}, err
	}
	bundleSum := fmt.Sprintf("%x", sha256.Sum256([]byte(bundle)))
	b.blobs[bundleSum] = blobSource{data: []byte(bundle)}
	pv := pushVersion{
		N: n, Commit: strings.TrimSpace(commit), Bundle: bundleSum, Title: info.Message,
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
		if !store.Has(objects, a.Sha256) {
			return pushVersion{}, fmt.Errorf("%s: %s is missing from .v2/objects", v, path)
		}
		pv.Files = append(pv.Files, pushFile{path, a.Sha256, a.Size, a.Source})
		b.blobs[a.Sha256] = blobSource{path: store.Path(objects, a.Sha256)}
	}
	if err := b.addCode(v, assets, &pv); err != nil {
		return pushVersion{}, err
	}
	if r, ok := assets[info.Render]; ok {
		p := probe(store.Path(objects, r.Sha256))
		pv.Duration, pv.Width, pv.Height, pv.FPS = p.duration, p.width, p.height, p.fps
		if jpg := poster(store.Path(objects, r.Sha256), p.duration/2, 1280); jpg != nil {
			pv.Poster = fmt.Sprintf("%x", sha256.Sum256(jpg))
			path := filepath.Join(b.tmp, pv.Poster+".jpg")
			if err := os.WriteFile(path, jpg, 0o644); err != nil {
				return pushVersion{}, err
			}
			b.blobs[pv.Poster] = blobSource{path: path}
		}
		// The site plays the original; only a big one also gets a 1080p copy for slower connections.
		if min(p.width, p.height) > 1080 || p.duration > 0 && float64(r.Size)*8/p.duration > 8e6 {
			mp4 := filepath.Join(b.gitDir, "v2-1080p", r.Sha256+".mp4")
			if err := copy1080(store.Path(objects, r.Sha256), mp4, len(b.noCopy) == 0); err != nil {
				// Every version fails the same way on a machine that can't encode, so each reason shows once.
				if !b.noCopy[err.Error()] {
					b.noCopy[err.Error()] = true
					fmt.Fprintf(os.Stderr, "warning: no 1080p copy for slower connections, so the site plays the full-size render: %v\n", err)
				}
			} else if sum, err := store.Hash(mp4); err == nil {
				pv.Preview = sum
				b.blobs[sum] = blobSource{path: mp4}
			}
		}
	}
	return pv, nil
}

// h264Encoders are the encoders the 1080p copy can be made with, best first: ffmpeg's software H.264
// encoders, since a browser plays H.264 everywhere and a hardware encoder depends on the machine.
var h264Encoders = [][]string{
	// crf 23 medium: measured half the error of vv's crf 25 veryfast at 1.7x the size.
	{"libx264", "-preset", "medium", "-crf", "23"},
	// Fedora's own ffmpeg (ffmpeg-free) has only this one, which has no crf. -qmin 23 holds it at about
	// the same quality, and it aims at no more than the 8 Mbit/s above which a render gets a copy at all.
	{"libopenh264", "-profile:v", "high", "-b:v", "8M", "-qmin", "23"},
}

// ffmpegAddress is the pointer in ffmpeg's "[libopenh264 @ 0xaaaad6748fc0] Unable to create encoder", which differs on every run.
var ffmpegAddress = regexp.MustCompile(` @ 0x[0-9a-f]+`)

// copy1080 encodes the 1080p copy. A dst already there is kept, so a push encodes each render once.
// say is false once a copy has failed, since the rest likely fail the same way and the warning said so.
func copy1080(src, dst string, say bool) error {
	if _, err := os.Stat(dst); err == nil {
		return nil
	}
	listed, _ := exec.Command("ffmpeg", "-v", "error", "-encoders").Output()
	i := slices.IndexFunc(h264Encoders, func(e []string) bool { return bytes.Contains(listed, []byte(" "+e[0]+" ")) })
	if i < 0 {
		return errors.New("this ffmpeg has no H.264 encoder (libx264 or libopenh264)")
	}
	if say {
		fmt.Println("making a 1080p copy for slower connections (a few minutes for a long render)")
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	defer func() { _ = os.Remove(tmp) }()
	args := append([]string{
		"-v", "error", "-y", "-i", src,
		"-vf", "scale='if(gt(iw,ih),-2,min(1080,iw))':'if(gt(iw,ih),min(1080,ih),-2)'", "-c:v",
	}, h264Encoders[i]...)
	args = append(args, "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "128k", "-movflags", "+faststart", "-f", "mp4", tmp)
	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		said, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n") // ffmpeg's first line names the cause; the rest follow from it
		return fmt.Errorf("ffmpeg: %s", cmp.Or(ffmpegAddress.ReplaceAllString(said, ""), err.Error()))
	}
	return os.Rename(tmp, dst) // a killed encode never leaves a half file to reuse
}

// maxCodeFiles keeps a repo with a committed vendor folder from breaking its video page, which reads
// every listed file. All the code is still in the version's bundle.
const maxCodeFiles = 1000

// addCode sends version v's git-tracked files as readable files, because the site can't open a bundle.
// .v2/ is v2's own bookkeeping.
func (b pushBuild) addCode(v string, assets map[string]asset, pv *pushVersion) error {
	entries, err := lsTree(b.dir, v)
	if err != nil {
		return err
	}
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
			f = pushFile{Sha256: fmt.Sprintf("%x", sha256.Sum256([]byte(data))), Size: int64(len(data)), Source: "code"}
			b.code[e.object] = f
			b.blobs[f.Sha256] = blobSource{data: []byte(data)}
		}
		f.Path = e.path
		pv.Files = append(pv.Files, f)
	}
	return nil
}

func (b blobSource) size() int64 {
	if b.path == "" {
		return int64(len(b.data))
	}
	fi, err := os.Stat(b.path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

// uploads remembers multipart uploads not yet completed, so a push that dies (network, Ctrl-C)
// resumes from its last finished part. Kept in .git/, never part of a version.
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
	return writeJSON(d.path, d)
}

// uploadSmall sends the in-memory blobs (code, bundles) together: each takes several round trips, so
// one at a time a project's code alone took 46 s. They are too small to need resuming.
func uploadSmall(l login, missing []string, blobs map[string]blobSource, sent *store.Progress, tmp string) error {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	slots := make(chan struct{}, 8)
	for _, h := range missing {
		if blobs[h].path != "" {
			continue
		}
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			err := uploadBlob(l, h, blobs[h], &store.Progress{}, &uploads{path: filepath.Join(tmp, h+".parts"), R2: map[string]*r2Upload{}})
			mu.Lock()
			defer mu.Unlock()
			if err != nil && firstErr == nil {
				firstErr = err
			}
			if err == nil {
				sent.Add(blobs[h].size())
			}
		}()
	}
	wg.Wait()
	return firstErr
}

var errNoUpload = errors.New("v2.video no longer has this file's unfinished upload; run `v2 push` again to start it over")

// uploadBlob sends a blob to R2 in parts of partSize, skipping parts an earlier push already
// sent, then has the site join them and register the file under its sha256.
func uploadBlob(l login, sum string, src blobSource, sent *store.Progress, done *uploads) error {
	var r io.ReaderAt = bytes.NewReader(src.data)
	size := src.size()
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
	up := done.R2[sum]
	if up == nil {
		up = &r2Upload{ETags: []string{}}
		if err := l.call("action", "cli:r2Start", map[string]any{"token": l.Token, "sha256": sum, "size": size, "contentType": ctype}, up); err != nil {
			return err
		}
		done.R2[sum] = up
		if err := done.save(); err != nil {
			return err
		}
	}
	forget := func() error {
		delete(done.R2, sum)
		return done.save()
	}
	sent.Add(min(int64(len(up.ETags))*partSize, size))
	for off := int64(len(up.ETags)) * partSize; off < size || len(up.ETags) == 0; off += partSize {
		n := min(partSize, size-off)
		etag, err := uploadPart(l, sum, up.UploadID, len(up.ETags)+1, io.NewSectionReader(r, off, n), sent)
		if errors.Is(err, errNoUpload) {
			err = errors.Join(err, forget())
		}
		if err != nil {
			return err
		}
		up.ETags = append(up.ETags, etag)
		if err := done.save(); err != nil {
			return err
		}
	}
	err := l.call("action", "cli:r2Complete", map[string]any{"token": l.Token, "sha256": sum, "size": size, "uploadId": up.UploadID, "etags": up.ETags}, nil)
	if !errors.Is(err, errUnreachable) {
		err = errors.Join(err, forget()) // a refused upload can't be reused either; the next push starts this file over
	}
	return err
}

// uploadPart PUTs one part to a fresh signed URL, retrying a failed attempt, and returns its ETag.
func uploadPart(l login, sum, uploadID string, number int, part *io.SectionReader, sent *store.Progress) (string, error) {
	var lastErr error
	for try := 1; try <= tries; try++ {
		var signed struct{ URLs []string }
		if err := l.call("action", "cli:r2Parts", map[string]any{"token": l.Token, "sha256": sum, "uploadId": uploadID, "parts": []int{number}}, &signed); err != nil {
			return "", err
		}
		if len(signed.URLs) != 1 {
			return "", fmt.Errorf("v2.video sent %d upload links for one part", len(signed.URLs))
		}
		before := sent.Done
		ctx, moved, settle := watchStall()
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, signed.URLs[0], io.TeeReader(io.NewSectionReader(part, 0, part.Size()), io.MultiWriter(sent, moved)))
		if err != nil {
			return "", err
		}
		req.ContentLength = part.Size()
		res, err := http.DefaultClient.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, res.Body)
			_ = res.Body.Close()
			etag := res.Header.Get("ETag")
			if res.StatusCode == http.StatusOK && etag != "" {
				_ = settle(nil)
				return etag, nil
			}
			if res.StatusCode == http.StatusNotFound { // R2 drops an unfinished upload after a week
				_ = settle(nil)
				return "", errNoUpload
			}
			err = fmt.Errorf("HTTP %d", res.StatusCode)
		}
		lastErr = settle(err)
		sent.Done = before
	}
	return "", fmt.Errorf("upload failed %d times (%w); run `v2 push` again to continue where it stopped", tries, lastErr)
}

// projectTitle names a new video on the site: README's first heading, else the folder's name.
func projectTitle(dir, readme string) string {
	if m := heading.FindStringSubmatch(readme); m != nil && strings.TrimSpace(m[1]) != "Video" {
		return strings.TrimSpace(m[1])
	}
	if title, _ := gitx.Run(dir, "config", "--get", "v2.forkTitle"); strings.TrimSpace(title) != "" {
		return strings.TrimSpace(title)
	}
	abs, _ := filepath.Abs(dir)
	return filepath.Base(abs)
}
