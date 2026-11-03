//go:build !darwin && !linux && !windows

package voice

import "errors"

// No engine is published for this OS (see Supported).

// Engine is unavailable here.
type Engine struct{}

// Load always fails here.
func Load(string) (*Engine, error) { return nil, errors.New("voice: not available on this OS") }

// Synthesize always fails here.
func (*Engine) Synthesize(string, int, float32) ([]float32, int, error) {
	return nil, 0, errors.New("voice: not available on this OS")
}

// Close does nothing here.
func (*Engine) Close() {}
