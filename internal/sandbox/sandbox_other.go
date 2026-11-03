//go:build !darwin && !linux && !windows

package sandbox

import "fmt"

func available() error {
	return fmt.Errorf("%w on this operating system (macOS, Linux and Windows only)", ErrUnavailable)
}

func wrap(Config, []string) ([]string, error) { return nil, available() }
