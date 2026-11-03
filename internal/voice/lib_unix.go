//go:build darwin || linux

package voice

import "github.com/ebitengine/purego"

// openLibrary loads the C API library; it finds onnxruntime beside it
// through its own rpath.
func openLibrary(path string) (uintptr, error) {
	return purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
}
