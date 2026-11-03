//go:build !windows

package sandbox

// Loopback reports whether sandboxed processes can connect to ackn4th on
// localhost. Only Windows needs a one-time setup for it.
func Loopback() error { return nil }

// Program returns argv unchanged: only Windows needs programs resolved
// (see the Windows version).
func Program(argv []string) ([]string, error) { return argv, nil }
