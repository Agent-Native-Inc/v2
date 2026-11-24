package media

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// h264Encoders are the encoders the 1080p copy can be made with, best first: ffmpeg's software H.264
// encoders, since a browser plays H.264 everywhere and a hardware encoder depends on the machine.
var h264Encoders = [][]string{
	// crf 23 medium: measured half the error of crf 25 veryfast at 1.7x the size.
	{"libx264", "-preset", "medium", "-crf", "23"},
	// Fedora's own ffmpeg (ffmpeg-free) has only this one, which has no crf. -qmin 23 holds it at about
	// the same quality, and it aims at no more than the 8 Mbit/s above which a render gets a copy at all.
	{"libopenh264", "-profile:v", "high", "-b:v", "8M", "-qmin", "23"},
}

// h264 is the best of h264Encoders this ffmpeg has, asked once per run.
var h264 = sync.OnceValues(func() ([]string, error) {
	listed, err := exec.Command("ffmpeg", "-v", "error", "-encoders").Output()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg can't list its encoders: %w", err)
	}
	i := slices.IndexFunc(h264Encoders, func(e []string) bool { return bytes.Contains(listed, []byte(" "+e[0]+" ")) })
	if i < 0 {
		return nil, errors.New("this ffmpeg has no H.264 encoder (libx264 or libopenh264)")
	}
	return h264Encoders[i], nil
})

// ffmpegAddress is the pointer in ffmpeg's "[libopenh264 @ 0xaaaad6748fc0] Unable to create encoder", which differs on every run.
var ffmpegAddress = regexp.MustCompile(` @ 0x[0-9a-f]+`)

// Copy1080 encodes a copy of src no bigger than 1080p, for slower connections. A dst already there is
// kept, so a push encodes each render once. say is false once a copy has failed, since the rest likely
// fail the same way and the warning said so.
func Copy1080(src, dst string, say bool) error {
	if _, err := os.Stat(dst); err == nil {
		return nil
	}
	encoder, err := h264()
	if err != nil {
		return err
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
	}, encoder...)
	args = append(args, "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "128k", "-movflags", "+faststart", "-f", "mp4", tmp)
	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		said, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n") // ffmpeg's first line names the cause; the rest follow from it
		return fmt.Errorf("ffmpeg: %s", cmp.Or(ffmpegAddress.ReplaceAllString(said, ""), err.Error()))
	}
	return os.Rename(tmp, dst) // a killed encode never leaves a half file to reuse
}
