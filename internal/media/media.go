// Package media runs ffmpeg and ffprobe: a video's shape, one frame, its loudness, and the 1080p copy
// push sends beside a big render. Each reader returns zeros or nil when ffmpeg can't read the file,
// so a caller shows less instead of failing.
package media

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"os/exec"
	"strconv"
	"strings"

	"github.com/Agent-Native-Inc/v2/internal/platform"
)

// Need errors, with how to install it, when ffmpeg or ffprobe is missing.
func Need() error {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			return errors.New("v2 needs ffmpeg (ffprobe comes with it). " + platform.InstallHint("ffmpeg") + ", then try again")
		}
	}
	return nil
}

// Info is a video's length and its shape as shown, after rotation.
type Info struct {
	Duration, Aspect, FPS float64
	Width, Height         int
}

// Probe asks ffprobe how long the video is and its shape as shown (a phone video stored
// landscape with a 90° rotation is portrait). Zeros if the file confuses it; the page then
// reads both from the video itself.
func Probe(path string) Info {
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries",
		"format=duration:stream=width,height,avg_frame_rate:stream_side_data_list", "-of", "json", path).Output()
	if err != nil {
		return Info{}
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
		return Info{}
	}
	var p Info
	p.Duration, _ = strconv.ParseFloat(r.Format.Duration, 64)
	if len(r.Streams) > 0 && r.Streams[0].Height > 0 {
		w, h := float64(r.Streams[0].Width), float64(r.Streams[0].Height)
		for _, sd := range r.Streams[0].SideData {
			if int(math.Abs(sd.Rotation))%180 == 90 {
				w, h = h, w
			}
		}
		p.Aspect, p.Width, p.Height = w/h, int(w), int(h)
		if num, den, ok := strings.Cut(r.Streams[0].FrameRate, "/"); ok {
			a, _ := strconv.ParseFloat(num, 64)
			b, _ := strconv.ParseFloat(den, 64)
			if b > 0 {
				p.FPS = a / b
			}
		}
	}
	return p
}

// Poster is the frame at `at` seconds as a JPEG, width pixels wide; nil if ffmpeg can't.
func Poster(path string, at float64, width int) []byte {
	out, err := exec.Command("ffmpeg", "-v", "error", "-ss", strconv.FormatFloat(at, 'f', 2, 64), "-i", path,
		"-frames:v", "1", "-vf", "scale=min("+strconv.Itoa(width)+"\\,iw):-2", "-f", "image2", "-c:v", "mjpeg", "-").Output()
	if err != nil || len(out) == 0 {
		return nil
	}
	return out
}

// Peaks is the sound's loudness, 100 values a second (the loudest sample in each 10 ms), as
// little-endian float32s: about 24 KB a minute. Empty if there's no sound.
func Peaks(path string) []byte {
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
