//go:build windows

package voice

import "golang.org/x/sys/windows"

// openLibrary loads the C API DLL, finding onnxruntime.dll in its own
// folder rather than on PATH.
func openLibrary(path string) (uintptr, error) {
	h, err := windows.LoadLibraryEx(path, 0, windows.LOAD_WITH_ALTERED_SEARCH_PATH)
	return uintptr(h), err
}
