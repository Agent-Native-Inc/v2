package main

import (
	"fmt"
	"regexp"
	"slices"
)

// Prompts, titles and changes go to v2.video, often public. These are the shapes of keys people paste into an agent.
var secretShapes = regexp.MustCompile(`sk-[A-Za-z0-9_-]{20,}|sk_[A-Za-z0-9]{32,}|AKIA[0-9A-Z]{16}|gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{20,}|xox[abprs]-[A-Za-z0-9-]{10,}|AIza[0-9A-Za-z_-]{35}|r8_[A-Za-z0-9]{20,}|hf_[A-Za-z0-9]{30,}|-----BEGIN [A-Z ]*PRIVATE KEY-----|eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.|(?i)(api[_-]?key|secret|token|password)\s*[:=]\s*\S{8,}`)

func refuseSecrets(flag string, values ...string) error {
	if slices.ContainsFunc(values, secretShapes.MatchString) {
		return fmt.Errorf("%s has what looks like a secret (an API key, token or password), and it would be pushed to v2.video. Replace it with [REDACTED] and save again", flag)
	}
	return nil
}
