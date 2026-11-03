package voice

import (
	"archive/tar"
	"compress/bzip2"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// The engine is sherpa-onnx (Apache-2.0); the model is Kokoro-82M v1.0
// (Apache-2.0), the full-precision build: on CPUs without fast int8
// support the quantized one is several times slower. Both are pinned and
// checked against their SHA-256 before anything is unpacked.

const engineVersion = "v2.0.1"

type asset struct {
	name   string // the release file
	sha256 string
	size   int64
}

var engineBase = "https://github.com/k2-fsa/sherpa-onnx/releases/download/" + engineVersion + "/"

var engines = map[string]asset{
	"darwin/amd64":  {"sherpa-onnx-v2.0.1-osx-x64-shared.tar.bz2", "100315fbb174de2d7fec92b67d28c4128499daf3a6f47b4da6350252680d178f", 22897549},
	"darwin/arm64":  {"sherpa-onnx-v2.0.1-osx-arm64-shared.tar.bz2", "5e874d3c9b3d6b8c71f7c09e3a9fb9ae846c3009129d302b398d1f7aedb69f84", 20348241},
	"linux/amd64":   {"sherpa-onnx-v2.0.1-linux-x64-shared.tar.bz2", "2e823376987c3b6960d50726b6560155e2ad92da0b8b3886325773225ec6bdec", 28003506},
	"linux/arm64":   {"sherpa-onnx-v2.0.1-linux-aarch64-shared-cpu.tar.bz2", "a8fc9fb3940d1aa3f83136767742b05068f00253f560d53c4f5aeb75ab4cb350", 27889232},
	"windows/amd64": {"sherpa-onnx-v2.0.1-win-x64-shared-MT-Release.tar.bz2", "dc703575a68594435f53dec93df775ff4913af851a196c0537163f21d8d2364e", 26664192},
	"windows/arm64": {"sherpa-onnx-v2.0.1-win-arm64-shared-MT-Release.tar.bz2", "d6faf59919314d4728a2701e715ea748a8a5d846fde1f1d58a38ecd91c848011", 25598348},
}

var modelBase = "https://github.com/k2-fsa/sherpa-onnx/releases/download/tts-models/"

var model = asset{"kokoro-multi-lang-v1_0.tar.bz2", "60703707bc05ccd48d60e1eb723f38b07d6e474795a0b6a588d7220400e0f7a4", 349886476}

// modelFile is the model's file in the archive and on disk.
const modelFile = "model.onnx"

// modelFiles are the model archive's files the voice needs: English only.
var modelFiles = []string{modelFile, "voices.bin", "tokens.txt", "lexicon-us-en.txt", "LICENSE"}

// Supported reports whether a voice engine is published for this OS and
// chip.
func Supported() error {
	if _, ok := engines[runtime.GOOS+"/"+runtime.GOARCH]; !ok {
		return fmt.Errorf("voice: no engine for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	return nil
}

// DownloadSize is the total download, in bytes, for this OS and chip.
func DownloadSize() int64 {
	return engines[runtime.GOOS+"/"+runtime.GOARCH].size + model.size
}

// Installed reports whether dir holds a usable engine and model.
func Installed(dir string) bool {
	_, err := files(dir)
	return err == nil
}

// Install downloads the engine and model into dir, verifies them and
// unpacks the files the voice needs. progress, if set, hears each step.
func Install(ctx context.Context, dir string, progress func(string)) error {
	if err := Supported(); err != nil {
		return err
	}
	say := func(s string) {
		if progress != nil {
			progress(s)
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	eng := engines[runtime.GOOS+"/"+runtime.GOARCH]
	keepLib := func(name string) (string, bool) {
		base := path.Base(name)
		if !strings.Contains(name, "/lib/") {
			return "", false
		}
		for _, ext := range []string{".dylib", ".so", ".dll"} {
			if strings.HasSuffix(base, ext) || strings.Contains(base, ".so.") {
				return filepath.Join(dir, "engine", base), true
			}
		}
		return "", false
	}
	keepModel := func(name string) (string, bool) {
		parts := strings.Split(strings.Trim(name, "/"), "/")
		if len(parts) < 2 {
			return "", false
		}
		rel := parts[1:]
		// Only the top-level files listed: not dict/ (Chinese) or espeak-ng-data
		// (unused since sherpa-onnx v2).
		if len(rel) == 1 && slices.Contains(modelFiles, rel[0]) {
			return filepath.Join(dir, "model", rel[0]), true
		}
		return "", false
	}
	say(fmt.Sprintf("downloading the speech engine (sherpa-onnx %s, %d MB)", engineVersion, eng.size>>20))
	if err := fetch(ctx, engineBase+eng.name, eng, keepLib); err != nil {
		return err
	}
	say(fmt.Sprintf("downloading the voice model (Kokoro v1.0, %d MB)", model.size>>20))
	if err := fetch(ctx, modelBase+model.name, model, keepModel); err != nil {
		return err
	}
	if !Installed(dir) {
		return errors.New("voice: the downloads didn't contain the expected files")
	}
	say("installed in " + dir)
	return nil
}

// fetch downloads a, checks its digest, then unpacks the entries keep
// maps to a destination.
func fetch(ctx context.Context, url string, a asset, keep func(string) (string, bool)) error {
	tmp, err := os.CreateTemp("", "ackn4th-voice-*.tar.bz2")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("voice: download %s: %w", a.name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("voice: download %s: %s", a.name, resp.Status)
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), resp.Body); err != nil {
		return fmt.Errorf("voice: download %s: %w", a.name, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != a.sha256 {
		return fmt.Errorf("voice: %s failed its checksum (got %s): not installed", a.name, got)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return unpack(tmp, keep)
}

// unpack writes the tar.bz2 entries keep selects.
func unpack(r io.Reader, keep func(string) (string, bool)) error {
	tr := tar.NewReader(bzip2.NewReader(r))
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("voice: unpack: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		dst, ok := keep(hdr.Name)
		if !ok {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(dst+".part", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, tr); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		if err := os.Rename(dst+".part", dst); err != nil {
			return err
		}
	}
}
