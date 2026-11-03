package voice

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
)

// player is the command that plays a WAV file on this OS, or an error
// naming what to install.
func player(ctx context.Context, wav string) (*exec.Cmd, error) {
	switch runtime.GOOS {
	case "darwin":
		return exec.CommandContext(ctx, "afplay", wav), nil
	case "windows":
		// .NET's SoundPlayer ships with Windows; PlaySync blocks until done.
		ps := "(New-Object Media.SoundPlayer $env:ACKN4TH_WAV).PlaySync()"
		cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", ps)
		cmd.Env = append(cmd.Environ(), "ACKN4TH_WAV="+wav)
		return cmd, nil
	}
	for _, p := range [][]string{{"pw-play"}, {"paplay"}, {"aplay", "-q"}} {
		if _, err := exec.LookPath(p[0]); err == nil {
			return exec.CommandContext(ctx, p[0], append(p[1:], wav)...), nil
		}
	}
	return nil, errors.New("voice: no audio player: install PipeWire (pw-play), PulseAudio (paplay) or ALSA (aplay)")
}

// Play plays a WAV file and returns when it ends or ctx is done.
func Play(ctx context.Context, wav string) error {
	cmd, err := player(ctx, wav)
	if err != nil {
		return err
	}
	if err := cmd.Run(); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

// CanPlay reports why audio can't be played here, if it can't.
func CanPlay() error {
	_, err := player(context.Background(), "")
	return err
}
