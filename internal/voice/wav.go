package voice

import (
	"encoding/binary"
	"io"
	"math"
)

// WriteWAV writes mono float samples as 16-bit PCM WAV.
func WriteWAV(w io.Writer, samples []float32, rate int) error {
	n := uint32(len(samples) * 2)
	hdr := []any{
		[4]byte{'R', 'I', 'F', 'F'}, 36 + n, [4]byte{'W', 'A', 'V', 'E'},
		[4]byte{'f', 'm', 't', ' '}, uint32(16), uint16(1), uint16(1), uint32(rate), uint32(rate * 2), uint16(2), uint16(16),
		[4]byte{'d', 'a', 't', 'a'}, n,
	}
	for _, v := range hdr {
		if err := binary.Write(w, binary.LittleEndian, v); err != nil {
			return err
		}
	}
	pcm := make([]int16, len(samples))
	for i, s := range samples {
		pcm[i] = int16(math.Round(float64(max(-1, min(1, s))) * math.MaxInt16))
	}
	return binary.Write(w, binary.LittleEndian, pcm)
}
