//go:build !windows

package sandbox

// Program returns argv unchanged: only Windows needs programs resolved
// (see the Windows version).
func Program(argv []string) ([]string, error) { return argv, nil }
