package main

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// change is one thing the agent says it did. From/To are seconds in this version's own
// timeline; both nil means it applies to the whole video (e.g. "voice 3 dB quieter").
// Kind and Src say what it is and which file made it ("voice", "assets/generated/vo.mp3"),
// so the versions page can lay changes out like an editor's tracks.
// A cut is kind "cut" with From == To where it now sits in this version.
type change struct {
	From    *float64 `json:"from,omitempty"`
	To      *float64 `json:"to,omitempty"`
	Kind    string   `json:"kind,omitempty"`
	Src     string   `json:"src,omitempty"`
	Type    string   `json:"type,omitempty"`    // "added" or "retimed"; empty means changed
	Removed float64  `json:"removed,omitempty"` // only on kind "cut": seconds of the previous version gone at From
	Text    string   `json:"text"`
}

var (
	tc       = `(?:\d+:){0,2}\d+(?:\.\d+)?` // 4, 4.5, 0:04, 0:04.5, 1:02:03
	cutRe    = regexp.MustCompile(`^cut\s+(` + tc + `)\s+(\d+(?:\.\d+)?)s\s+(.+)$`)
	rangeRe  = regexp.MustCompile(`^(` + tc + `)\s*[-–]\s*(` + tc + `)\s+(?:(added|retimed)\s+)?(.+)$`)
	momentRe = regexp.MustCompile(`^(` + tc + `)\s+(?:(added|retimed)\s+)?(.+)$`)
	sourceRe = regexp.MustCompile(`^(text|captions|graphics|video|voice|music|sfx)\s+(.+?):\s+(.+)$`)
)

// sourced splits "voice assets/generated/vo.mp3: new intro voice-over" into kind, file and words.
// Text without a kind and a file before the colon is kept whole.
func sourced(from, to *float64, typ, text string) change {
	if m := sourceRe.FindStringSubmatch(text); m != nil {
		return change{From: from, To: to, Kind: m[1], Src: m[2], Type: typ, Text: m[3]}
	}
	return change{From: from, To: to, Type: typ, Text: text}
}

// parseChanges reads --change lines:
//
//	"0:04.5-0:06.5 zooms in on the product name"   a span
//	"0:12 logo card appears"                         a moment
//	"cut 0:03.4 3.4s removed the slow intro"         a cut
//	"voice 3 dB quieter"                             the whole video
//
// After the time, "<kind> <file>: " says what changed and which file made it:
//
//	"0:02-0:09 voice assets/generated/vo-intro.mp3: voice-over for the intro"
//
// Right after the time, "added" or "retimed" says the change is new or only moved in time:
//
//	"0:12-0:15 added video assets/generated/end.mp4: final screen"
func parseChanges(lines []string) ([]change, error) {
	var changes []change
	for _, line := range lines {
		line = strings.TrimSpace(line)
		cm, rm, mm := cutRe.FindStringSubmatch(line), rangeRe.FindStringSubmatch(line), momentRe.FindStringSubmatch(line)
		switch {
		case line == "":
		case cm != nil:
			at, removed := seconds(cm[1]), seconds(cm[2])
			if removed <= 0 {
				return nil, fmt.Errorf("%q: a cut has to remove more than 0 seconds", line)
			}
			changes = append(changes, change{From: &at, To: &at, Kind: "cut", Removed: removed, Text: cm[3]})
		case strings.HasPrefix(line, "cut "):
			return nil, fmt.Errorf("%q: write cuts as \"cut <where> <seconds>s <what>\", e.g. \"cut 0:03.4 3.4s removed the slow intro\"", line)
		case rm != nil:
			from, to := seconds(rm[1]), seconds(rm[2])
			if to < from {
				return nil, fmt.Errorf("%q: the span ends before it starts", line)
			}
			changes = append(changes, sourced(&from, &to, rm[3], rm[4]))
		case mm != nil:
			at := seconds(mm[1])
			changes = append(changes, sourced(&at, &at, mm[2], mm[3]))
		default:
			changes = append(changes, sourced(nil, nil, "", line))
		}
	}
	return changes, nil
}

// seconds reads 4, 4.5, 0:04.5 or 1:02:03.5. The regexes above already checked the shape.
func seconds(s string) float64 {
	var total float64
	for p := range strings.SplitSeq(s, ":") {
		f, _ := strconv.ParseFloat(p, 64)
		total = total*60 + f
	}
	return total
}

// timecode prints seconds the way people write them: 0:04, 0:04.5, 1:02:03.
func timecode(s float64) string {
	s = math.Round(s*100) / 100 // before splitting, so 59.996 is 1:00, not 0:60
	minutes := int(s) / 60
	secs := s - float64(minutes*60)
	secText := strconv.FormatFloat(secs, 'f', 2, 64)
	secText = strings.TrimRight(strings.TrimRight(secText, "0"), ".")
	if secs < 10 {
		secText = "0" + secText
	}
	if minutes >= 60 {
		return fmt.Sprintf("%d:%02d:%s", minutes/60, minutes%60, secText)
	}
	return fmt.Sprintf("%d:%s", minutes, secText)
}

// describe turns the changes back into one readable line, for the git message and `v2 log`.
func describe(changes []change) string {
	var parts []string
	for _, c := range changes {
		text := strings.TrimSpace(c.Type + " " + c.Text)
		switch {
		case c.Kind == "cut":
			parts = append(parts, fmt.Sprintf("cut %s %ss %s", timecode(*c.From), strconv.FormatFloat(c.Removed, 'f', -1, 64), c.Text))
		case c.From == nil:
			parts = append(parts, c.Text)
		case *c.From == *c.To:
			parts = append(parts, timecode(*c.From)+" "+text)
		default:
			parts = append(parts, timecode(*c.From)+"-"+timecode(*c.To)+" "+text)
		}
	}
	return strings.Join(parts, "; ")
}
