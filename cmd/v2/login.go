package main

// v2 login / logout: sign this computer in to v2.video with a code approved in the browser.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Agent-Native-Inc/v2/internal/store"
)

// whoami is what cli:whoami says about the signed-in account; nil until a login is approved.
// A site too old to send the storage numbers leaves them nil.
type whoami struct {
	Name, Email             string
	StoredBytes, LimitBytes *float64
}

func runLogin() error {
	server := envOr("V2_SERVER", defaultServer)
	// Agents run login before every push, so a login that still works ends here instead of printing a code and waiting for an approval.
	if old, err := readLogin(); err == nil && old.Server == server {
		var who *whoami
		if old.call("query", "cli:whoami", old.authed(map[string]any{}), &who) == nil && who != nil {
			fmt.Printf("Already signed in as %s (%s). To switch accounts, run `v2 logout` first.\n", who.Name, who.Email)
			return nil
		}
	}
	b := make([]byte, 24)
	rand.Read(b)
	l := login{Server: server, Site: envOr("V2_SITE", defaultSite), Token: "v2t_" + hex.EncodeToString(b)}
	var start struct{ Code, URL string }
	if err := l.call("mutation", "cli:startLogin", map[string]any{"tokenHash": store.Sum([]byte(l.Token))}, &start); err != nil {
		return err
	}
	fmt.Printf("Open %s and approve code %s there\n", l.Site+start.URL, start.Code)
	openBrowser(l.Site + start.URL)
	var down error // the last check, when it got no answer: the approval can still land, so login keeps waiting
	for deadline := time.Now().Add(10 * time.Minute); time.Now().Before(deadline); time.Sleep(2 * time.Second) {
		var who *whoami
		err := l.call("query", "cli:whoami", l.authed(map[string]any{}), &who)
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
		if err := saveLogin(l); err != nil {
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

func saveLogin(l login) error {
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
	return os.WriteFile(path, j, 0o600)
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
	if err := l.call("mutation", "cli:logout", l.authed(map[string]any{}), nil); err != nil {
		return fmt.Errorf("signed out on this computer, but v2.video couldn't be told: %w", err)
	}
	return nil
}
