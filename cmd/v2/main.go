// Command v2 is version control for AI video projects: the code and text through git, and the media
// in its own store.
package main

import (
	"os"

	"github.com/agentnativeinc/v2/internal/media"
	"github.com/agentnativeinc/v2/internal/platform"
	"github.com/spf13/cobra"
)

// version is set at release build time: go build -ldflags "-X main.version=v0.1.0".
var version = "dev"

func main() {
	root := &cobra.Command{
		Use:   "v2",
		Short: "Version control for AI video projects",
		Long: `v2 saves every version of a video project: the code and text through git,
and the footage, music and renders in its own store, so any version comes back exactly.

Getting started:
  cd your-video-project     (or a new empty folder)
  v2 init                   sets it up and teaches Claude Code / Codex to use v2 (AGENTS.md)
                            run it in the video's own folder, never in ~, Desktop or Documents
  claude                    then ask for edits as usual; it saves a version after each one
  v2 diff                   watch every version in the browser, compare two, leave timed comments

Needs git and ffmpeg. ` + platform.InstallHint("ffmpeg") + `.`,
		Version:      version,
		SilenceUsage: true, // on error, print just the error, not the whole help text
		PersistentPreRun: func(cmd *cobra.Command, _ []string) {
			if cmd.Name() != "update" {
				notifyUpdate()
			}
		},
	}

	root.AddCommand(
		&cobra.Command{
			Use:   "init",
			Short: "Start tracking this folder",
			Args:  cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error {
				if err := media.Need(); err != nil {
					return err
				}
				return runInit(".")
			},
		},
		&cobra.Command{
			Use:   "clone <address> [dir]",
			Short: "Download a project from v2.video with all its versions (owner/slug or its link)",
			Args:  cobra.RangeArgs(1, 2),
			RunE: func(_ *cobra.Command, args []string) error {
				dir := ""
				if len(args) == 2 {
					dir = args[1]
				}
				return runClone(args[0], dir)
			},
		},
		importCmd(),
		saveCmd(),
		diffCmd(),
		&cobra.Command{
			Use:   "log",
			Short: "List saved versions, newest first",
			Args:  cobra.NoArgs,
			RunE:  func(*cobra.Command, []string) error { return runLog(".") },
		},
		&cobra.Command{
			Use:   "comments",
			Short: "List open comments from the v2 diff page and v2.video",
			Args:  cobra.NoArgs,
			RunE:  func(*cobra.Command, []string) error { return runComments(".") },
		},
		&cobra.Command{
			Use:   "login",
			Short: "Sign in to v2.video in the browser, so push can upload",
			Args:  cobra.NoArgs,
			RunE:  func(*cobra.Command, []string) error { return runLogin() },
		},
		&cobra.Command{
			Use:   "logout",
			Short: "Sign this computer out of v2.video",
			Args:  cobra.NoArgs,
			RunE:  func(*cobra.Command, []string) error { return runLogout() },
		},
		&cobra.Command{
			Use:   "push",
			Short: "Upload every saved version to v2.video (private) and print its link",
			Args:  cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error {
				if err := media.Need(); err != nil {
					return err
				}
				return runPush(".")
			},
		},
		&cobra.Command{
			Use:   "publish",
			Short: "Make this project public on v2.video, so anyone with its link can watch it",
			Args:  cobra.NoArgs,
			RunE:  func(*cobra.Command, []string) error { return runVisibility(".", "public") },
		},
		&cobra.Command{
			Use:   "unpublish",
			Short: "Make this project private again: only you can watch it",
			Args:  cobra.NoArgs,
			RunE:  func(*cobra.Command, []string) error { return runVisibility(".", "private") },
		},
		&cobra.Command{
			Use:   "restore <vN>",
			Short: "Bring the folder back to version N exactly (saved as a new version)",
			Args:  cobra.ExactArgs(1),
			RunE:  func(_ *cobra.Command, args []string) error { return runRestore(".", args[0]) },
		},
		&cobra.Command{
			Use:   "update",
			Short: "Replace this v2 with the latest release, checked against v2.video's signature",
			Args:  cobra.NoArgs,
			RunE:  func(*cobra.Command, []string) error { return runUpdate() },
		},
	)

	// Execute only returns the error; without this the process exits 0 and agents think it worked.
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func importCmd() *cobra.Command {
	var as string
	c := &cobra.Command{
		Use:   "import <files>...",
		Short: "Bring footage into the project (lands in assets/imported/<name>)",
		Args:  cobra.MinimumNArgs(1),
		RunE:  func(_ *cobra.Command, args []string) error { return runImport(".", args, as) },
	}
	c.Flags().StringVar(&as, "as", "", "name to use inside assets/ (one file only)")
	return c
}

func saveCmd() *cobra.Command {
	var message, model, assemble, render string
	var prompts, change, resolves []string
	c := &cobra.Command{
		Use:   "save",
		Short: "Save a new version (v1, v2, ...)",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			changes, err := checkSaveFlags(message, prompts, change)
			if err != nil {
				return err
			}
			_, err = runSave(".", saveInput{message: message, prompts: prompts, model: model, changes: changes, assemble: assemble, render: render, resolves: resolves})
			return err
		},
	}
	c.Flags().StringVarP(&message, "message", "m", "", "title: a one-line summary of what this version is")
	c.Flags().StringArrayVar(&prompts, "prompt", nil, "one of the user's messages that asked for this version, word for word, repeatable")
	c.Flags().StringVar(&model, "model", "", "the model that made this version, e.g. claude-opus-5-5")
	c.Flags().StringArrayVar(&change, "change", nil, "one change, repeatable: \"0:04-0:09 video assets/generated/broll.mp4: new b-roll\", \"cut 0:03.4 3.4s removed the slow intro\", or no time for the whole video")
	c.Flags().StringVar(&assemble, "assemble", "", "command that rebuilds the final video (overrides .v2/config.json for this save)")
	c.Flags().StringVar(&render, "render", "", "the rendered video for this version (overrides .v2/config.json for this save)")
	c.Flags().StringArrayVar(&resolves, "resolves", nil, "a comment this version fixes, repeatable: --resolves c3")
	return c
}

func diffCmd() *cobra.Command {
	var port int
	var noOpen bool
	c := &cobra.Command{
		Use:   "diff",
		Short: "Open every version in the browser, to watch one or compare two",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if err := media.Need(); err != nil {
				return err
			}
			return runDiff(".", port, !noOpen)
		},
	}
	c.Flags().IntVar(&port, "port", 0, "port to serve on (default: any free one)")
	c.Flags().BoolVar(&noOpen, "no-open", false, "print the address instead of opening the browser")
	return c
}
