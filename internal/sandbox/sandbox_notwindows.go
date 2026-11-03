//go:build !windows || !winsandbox

package sandbox

// Loopback reports whether sandboxed processes can connect to ackn4th on
// localhost. Only Windows needs a one-time setup for it.
func Loopback() error { return nil }
