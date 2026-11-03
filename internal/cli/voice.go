package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dethmasque/ackn4th/internal/config"
	"github.com/dethmasque/ackn4th/internal/voice"
)

// voiceSettings are the configured voice's.
func (a *app) voiceSettings() voice.Settings {
	return voice.Settings{Voice: a.cfg.Voice.Name, Speed: a.cfg.Voice.Speed}
}

// installVoice downloads the voice unless it is already installed.
func (a *app) installVoice(ctx context.Context) (string, error) {
	dir, err := config.VoiceDir()
	if err != nil {
		return "", err
	}
	if voice.Installed(dir) {
		return dir, nil
	}
	if err := voice.Supported(); err != nil {
		return "", err
	}
	a.term.Println(a.term.Gray(fmt.Sprintf("  one-time download, %d MB, into %s", voice.DownloadSize()>>20, dir)))
	err = voice.Install(ctx, dir, func(s string) { a.term.Println(a.term.Gray("  · " + s)) })
	return dir, err
}

func voiceCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "voice",
		GroupID: "info",
		Short:   "Have the dashboard read itself aloud (accessibility)",
		Long: `The dashboard can speak: where you are, what is selected, what changed
and what each key does, for people who can't easily read the screen.

The voice runs on this computer, with no account or API key: the
open-source sherpa-onnx engine with the Kokoro-82M model (both
Apache-2.0), downloaded once (about 350 MB, checked against pinned
SHA-256 digests) into the OS's cache directory. Nothing you hear leaves
your computer.

In the dashboard, v turns it on or off and V repeats the last phrase.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	var name string
	var speed float32
	on := &cobra.Command{
		Use:   "on",
		Short: "Turn the voice on (installing it first if needed)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if name != "" && voice.SpeakerID(name) < 0 {
				return fmt.Errorf("no voice %q: see `ackn4th voice list`", name)
			}
			if speed != 0 && (speed < 0.5 || speed > 2) {
				return fmt.Errorf("--speed must be between 0.5 and 2")
			}
			if _, err := a.installVoice(cmd.Context()); err != nil {
				return err
			}
			if err := voice.CanPlay(); err != nil {
				return err
			}
			path, err := config.SetKey("voice.enabled", true)
			if err != nil {
				return err
			}
			if name != "" {
				if _, err := config.SetKey("voice.name", name); err != nil {
					return err
				}
			}
			if speed != 0 {
				if _, err := config.SetKey("voice.speed", speed); err != nil {
					return err
				}
			}
			a.term.Println(a.term.Green("✓ "), "the dashboard will speak ", a.term.Gray("("+path+"; v toggles it there)"))
			return nil
		},
	}
	on.Flags().StringVar(&name, "name", "", "the `voice`, e.g. af_heart (ackn4th voice list shows them all)")
	on.Flags().Float32Var(&speed, "speed", 0, "speaking `rate`, 0.5 to 2 (1 is normal)")

	off := &cobra.Command{
		Use:   "off",
		Short: "Turn the voice off",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := config.SetKey("voice.enabled", false)
			if err != nil {
				return err
			}
			a.term.Println(a.term.Green("✓ "), "the voice is off ", a.term.Gray("("+path+")"))
			return nil
		},
	}

	install := &cobra.Command{
		Use:   "install",
		Short: "Download the voice's engine and model",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := a.installVoice(cmd.Context())
			if err != nil {
				return err
			}
			a.term.Println(a.term.Green("✓ "), "the voice is installed ", a.term.Gray("("+dir+"; `ackn4th voice on` to use it)"))
			return nil
		},
	}

	uninstall := &cobra.Command{
		Use:   "uninstall",
		Short: "Delete the downloaded engine, model and spoken phrases",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := config.VoiceDir()
			if err != nil {
				return err
			}
			if err := os.RemoveAll(dir); err != nil {
				return err
			}
			if a.cfg.Voice.Enabled {
				if _, err := config.SetKey("voice.enabled", false); err != nil {
					return err
				}
			}
			a.term.Println(a.term.Green("✓ "), "removed ", a.term.Gray(dir))
			return nil
		},
	}

	test := &cobra.Command{
		Use:   "test [text]",
		Short: "Say something with the configured voice",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			text := "Hello. This is the ackn4th dashboard voice."
			if len(args) == 1 {
				text = args[0]
			}
			dir, err := config.VoiceDir()
			if err != nil {
				return err
			}
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			a.term.Println(a.term.Gray("  loading the voice (a few seconds)…"))
			return voice.SayOnce(cmd.Context(), exe, dir, a.voiceSettings(), text)
		},
	}

	status := &cobra.Command{
		Use:   "status",
		Short: "Show whether the voice is installed and on",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := config.VoiceDir()
			if err != nil {
				return err
			}
			installed, enabled := "no (`ackn4th voice install`)", "off"
			if voice.Installed(dir) {
				installed = "yes, in " + dir
			}
			if a.cfg.Voice.Enabled {
				enabled = "on"
			}
			s := a.voiceSettings()
			name := s.Voice
			if name == "" {
				name = voice.DefaultVoice
			}
			speed := s.Speed
			if speed == 0 {
				speed = 1
			}
			player := "ok"
			if err := voice.CanPlay(); err != nil {
				player = err.Error()
			}
			a.term.Box("VOICE", a.term.KV(
				[2]string{"installed", installed},
				[2]string{"dashboard", enabled},
				[2]string{"voice", name},
				[2]string{"speed", fmt.Sprintf("%.2g", speed)},
				[2]string{"audio", player},
			))
			return nil
		},
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List the voices",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			groups := map[string]string{"af": "American, female", "am": "American, male", "bf": "British, female", "bm": "British, male"}
			for _, g := range []string{"af", "am", "bf", "bm"} {
				var names []string
				for _, v := range voice.Voices {
					if strings.HasPrefix(v, g+"_") {
						if v == voice.DefaultVoice {
							v += " (default)"
						}
						names = append(names, v)
					}
				}
				a.term.Println(a.term.Bold(fmt.Sprintf("  %-18s", groups[g])), strings.Join(names, "  "))
			}
			a.term.Println(a.term.Gray("  try one: ackn4th voice on --name am_michael && ackn4th voice test"))
			return nil
		},
	}
	cmd.AddCommand(on, off, install, uninstall, test, status, list)
	return cmd
}
