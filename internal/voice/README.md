# The voice

[← README](../../README.md)

An accessibility option: the [dashboard](../dash/README.md) reads itself
aloud. The voice is realistic, runs entirely on your computer, and needs no
account or API key.

```sh
ackn4th voice on                    # installs it the first time, then the dashboard speaks
ackn4th voice test "Hello there"    # hear it
ackn4th voice list                  # the voices
ackn4th voice on --name am_michael --speed 1.1
ackn4th voice status
ackn4th voice off
ackn4th voice uninstall             # delete the download
```

In the dashboard, `v` turns it on or off (the welcome dialog offers it on
first run) and `V` repeats the last phrase. The setting is saved as
`voice:` in the [config](../config/README.md). To have the dashboard speak
from its very first screen without changing the setting, open it with
`ackn4th --voice` (also `ackn4th dash --voice` and `ackn4th tour --voice`).

## What it is

- **Engine:** [sherpa-onnx](https://github.com/k2-fsa/sherpa-onnx) v2.0.1
  (Apache-2.0), its C library with ONNX Runtime.
- **Model:** [Kokoro-82M](https://huggingface.co/hexgrad/Kokoro-82M) v1.0
  (Apache-2.0), full precision: on CPUs without fast int8 support the
  quantized model is several times slower. The default voice is `af_heart`.
  There are 28 English voices: American and British, female and male.

Kokoro is about the smallest model that sounds natural: 82 million
parameters, a short phrase in about a second on a laptop CPU. Nothing is
sent anywhere.

## Install

`ackn4th voice install` (or turning the voice on) downloads, once:

| File | From | Size |
| --- | --- | --- |
| `sherpa-onnx-v2.0.1-<os>-<arch>-shared….tar.bz2` | [sherpa-onnx releases](https://github.com/k2-fsa/sherpa-onnx/releases/tag/v2.0.1) | 20–27 MB |
| `kokoro-multi-lang-v1_0.tar.bz2` | [sherpa-onnx `tts-models`](https://github.com/k2-fsa/sherpa-onnx/releases/tag/tts-models) | 334 MB |

Each is checked against a SHA-256 digest pinned in `install.go` before
anything is unpacked, and only the files the voice needs are kept: the
engine's shared libraries, and the model, its voices, tokens, US English
lexicon and license. They go in the OS's cache directory
(`~/Library/Caches/ackn4th/voice` on macOS, `~/.cache/ackn4th/voice` on
Linux, `%LOCALAPPDATA%\ackn4th\voice` on Windows), with the spoken phrases
cached beside them.

Supported: macOS, Linux and Windows, on amd64 and arm64. Audio plays with
`afplay` on macOS, PowerShell's `SoundPlayer` on Windows, and `pw-play`
(PipeWire), `paplay` (PulseAudio) or `aplay` (ALSA) on Linux, whichever is
installed. A container has no audio, so the voice is for ackn4th installed
on your machine.

## How it works

ackn4th is built without cgo, so it loads the engine's C library at run
time with [purego](https://github.com/ebitengine/purego); `engine.go`
mirrors the C API's structs. The engine runs in a **helper process**
(`ackn4th __voice`): the library writes diagnostics to stderr, which would
scribble over the dashboard, and a crash in it takes down only the voice.
The dashboard sends the helper one JSON line per phrase.

sherpa-onnx v2 has no phonemizer for English beyond the lexicon, which
lacks many inflections ("agent" but not "agents"). So ackn4th phonemizes
the text itself (`phonemes.go`): words from the lexicon (plus a few
software words it lacks, like "dev"), regular -s, -ed and -ing forms
derived from it, compounds of two of its words ("backend" is back + end),
anything else spelled out. `text.go` first rewrites what a lexicon can't
say: numbers, task IDs, acronyms, symbols, and names like `TECH_LEAD`,
which are words, not an acronym.

A phrase is spoken a sentence at a time: the first sentence plays as soon
as it is synthesized, and the next ones are synthesized while it plays, so
a long description starts as quickly as a short one. Sentences are cached
as WAV files, so one said before plays at once. The cache is capped at
128 MB (a sentence is around 400 KB): when it grows past that, the
sentences heard least recently are deleted, so the dashboard's own
phrases stay and one-off text (a task's prompt, an event) ages out. A new phrase cuts off the
one playing, and one superseded within 150 ms is never synthesized.

## Updating the engine or model

1. Pick the new release, and read its notes for C API changes: the structs
   in `engine.go` must match its `c-api.h` field for field (it ships in the
   archive's `include/`).
2. Update `engineVersion`, the file names, SHA-256 digests and sizes in
   `install.go`. The digests are in the GitHub API's release assets
   (`gh api repos/k2-fsa/sherpa-onnx/releases/tags/<tag> --jq '.assets[] | [.name, .digest, .size]'`).
3. Try it end to end with the archives downloaded to a directory:
   `ACKN4TH_VOICE_ASSETS=<dir> ACKN4TH_VOICE_PLAY=1 go test -v -run InstallAndSpeak ./internal/voice`
   (`ACKN4TH_VOICE_DIR=<install>` reuses an install instead).
