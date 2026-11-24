package main

import (
	"errors"
	"fmt"

	"github.com/agentnativeinc/v2/internal/gitx"
)

func runVisibility(dir, visibility string) error {
	dir, err := findProject(dir)
	if err != nil {
		return err
	}
	videoID := gitx.Config(dir, "v2.video")
	if videoID == "" {
		return errors.New("this project isn't on v2.video yet. Run v2 push first, then try again")
	}
	l, err := readLogin()
	if err != nil {
		return err
	}
	var out struct {
		Changed bool   `json:"changed"`
		URL     string `json:"url"`
	}
	if err := l.call("mutation", "cli:setVisibility", l.authed(map[string]any{"videoId": videoID, "visibility": visibility}), &out); err != nil {
		return err
	}
	link := l.Site + out.URL
	switch {
	case visibility == "public" && out.Changed:
		fmt.Printf("public: anyone with the link can watch it: %s\n", link)
	case visibility == "public":
		fmt.Printf("already public: %s\n", link)
	case out.Changed:
		fmt.Printf("private: only you can watch it now. Links you shared stop working: %s\n", link)
	default:
		fmt.Printf("already private: %s\n", link)
	}
	return nil
}
