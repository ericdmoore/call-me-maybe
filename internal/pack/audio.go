package pack

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"

	"callmemaybe/internal/voice"
)

// Unlike the permissive TTS decoder, downloaded playback assets must be whole
// RIFF files with one PCM format and one aligned, nonempty data chunk.
func validateWAV(b []byte, rate int) (string, error) {
	if len(b) < 44 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" || uint64(binary.LittleEndian.Uint32(b[4:8]))+8 != uint64(len(b)) {
		return "", errors.New("invalid or truncated RIFF/WAVE")
	}
	haveFmt, haveData := false, false
	var pcm []byte
	for off := 12; off < len(b); {
		if len(b)-off < 8 {
			return "", errors.New("truncated WAV chunk")
		}
		size := uint64(binary.LittleEndian.Uint32(b[off+4 : off+8]))
		body := off + 8
		if size > uint64(len(b)-body) {
			return "", errors.New("truncated WAV payload")
		}
		end := body + int(size)
		switch string(b[off : off+4]) {
		case "fmt ":
			if haveFmt || size < 16 {
				return "", errors.New("invalid WAV format chunk")
			}
			haveFmt = true
			if binary.LittleEndian.Uint16(b[body:]) != 1 || binary.LittleEndian.Uint16(b[body+2:]) != 1 || binary.LittleEndian.Uint32(b[body+4:]) != uint32(rate) || binary.LittleEndian.Uint32(b[body+8:]) != uint32(rate*2) || binary.LittleEndian.Uint16(b[body+12:]) != 2 || binary.LittleEndian.Uint16(b[body+14:]) != 16 {
				return "", fmt.Errorf("want %d Hz mono 16-bit PCM", rate)
			}
		case "data":
			if haveData || size == 0 || size%2 != 0 {
				return "", errors.New("invalid WAV data chunk")
			}
			haveData = true
			pcm = b[body:end]
		}
		off = end + int(size%2)
		if off > len(b) {
			return "", errors.New("missing WAV chunk padding")
		}
	}
	if !haveFmt || !haveData {
		return "", errors.New("WAV needs format and data")
	}
	level := voice.DBFS(voice.Audio{PCM: pcm, SampleRate: rate})
	if math.IsInf(level, -1) {
		return "", errors.New("clip is digital silence")
	}
	peak := 0.0
	for i := 0; i < len(pcm); i += 2 {
		v := math.Abs(float64(int16(binary.LittleEndian.Uint16(pcm[i:]))))
		if v > peak {
			peak = v
		}
	}
	peakDB := 20 * math.Log10(peak/32767)
	// RMS is not LUFS. A crest-limited or human-recorded clip can legitimately
	// miss the RMS target. Report it, never silently rewrite the recording.
	if level < -24 || level > -12 || peakDB > -1.5 {
		return fmt.Sprintf("level %.1f dBFS RMS, peak %.1f dBFS; speech target -18 ±6 dBFS RMS, peak ≤-2 dBFS (0.5 dB tolerance)", level, peakDB), nil
	}
	return "", nil
}
