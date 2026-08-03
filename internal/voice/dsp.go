package voice

import (
	"bytes"
	"encoding/binary"
	"math"
)

// The shared stage every backend's output passes through: resample to the two
// rates a pack needs, even out the loudness, write the WAV pair.
//
// Pure Go rather than shelling out to ffmpeg, because the promise of this
// program is one file you copy to a machine, and "install ffmpeg first" is a
// worse answer for a pack author than for a shell script. prompts/build.sh
// still uses ffmpeg and still works; this is the path `doorman pack build`
// takes.
//
// What that trades away is honest to state: ffmpeg's loudnorm implements EBU
// R128, which models perceived loudness with a K-weighting filter and gating.
// Normalise below is RMS with true-peak limiting, which is simpler and not the
// same thing. At 8 kHz over a telephone — 300–3400 Hz of bandwidth, a codec
// designed in 1972, and a handset speaker — the difference is far below what
// the channel can carry. For a music bed it would matter; for six spoken
// prompts it does not.

// PackRates are the two a pack ships: 8 kHz for ulaw calls, 16 kHz for g722.
// Asterisk plays whichever needs less transcoding.
var PackRates = []int{8000, 16000}

// TargetDBFS is the RMS level every clip is brought to.
//
// -18 matches what prompts/build.sh asks loudnorm for, so packs built either
// way sit at the same level. Nothing is worse than a greeting you can barely
// hear followed by a "Good day." that peaks the line.
const TargetDBFS = -18.0

// peakCeilingDBFS leaves headroom below full scale. Resampling can overshoot
// the original peak by a little, and a sample that clips on the way out is a
// click on every call forever.
const peakCeilingDBFS = -2.0

// samples reads the PCM as signed 16-bit.
func (a Audio) samples() []int16 {
	out := make([]int16, len(a.PCM)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(a.PCM[i*2:]))
	}
	return out
}

func fromSamples(s []int16, rate int) Audio {
	b := make([]byte, len(s)*2)
	for i, v := range s {
		binary.LittleEndian.PutUint16(b[i*2:], uint16(v))
	}
	return Audio{PCM: b, SampleRate: rate}
}

// Resample converts to rate using windowed-sinc interpolation.
//
// Band-limited rather than linear because every rate here is a downsample —
// 22.05 kHz or 24 kHz down to 16 or 8 — and a downsample without a low-pass
// folds everything above the new Nyquist back into the audible band as
// aliasing. On speech that sounds like a metallic lisp, and it is the kind of
// artefact people describe as "the cheap one" without being able to say why.
func Resample(in Audio, rate int) Audio {
	if rate <= 0 || in.SampleRate == rate || len(in.PCM) < 2 {
		out := in
		out.SampleRate = rate
		if rate <= 0 || in.SampleRate == rate {
			return in
		}
		return out
	}

	src := in.samples()
	ratio := float64(rate) / float64(in.SampleRate)

	// Cutoff in cycles per input sample. Downsampling has to filter to the
	// *output* Nyquist, which is what stops the aliasing.
	cutoff := 0.5
	if ratio < 1 {
		cutoff = 0.5 * ratio
	}

	// A narrower filter in frequency is a longer one in time, so the kernel
	// grows as the cutoff drops. Sixteen zero crossings either side is well
	// past transparent for speech.
	const zeroCrossings = 16
	half := zeroCrossings / (2 * cutoff)

	n := int(float64(len(src)) * ratio)
	dst := make([]int16, n)

	for i := range dst {
		centre := float64(i) / ratio
		lo := int(math.Ceil(centre - half))
		hi := int(math.Floor(centre + half))

		var sum, norm float64
		for j := lo; j <= hi; j++ {
			d := centre - float64(j)
			w := blackman(d / half)
			if w == 0 {
				continue
			}
			h := 2 * cutoff * sinc(2*cutoff*d) * w
			// Clamp at the edges rather than treating outside as silence,
			// which would fade the first and last few milliseconds.
			k := j
			if k < 0 {
				k = 0
			}
			if k >= len(src) {
				k = len(src) - 1
			}
			sum += float64(src[k]) * h
			norm += h
		}
		// Normalising by the kernel's own sum keeps unity gain regardless of
		// where the taps fell, which matters at the ends.
		if norm != 0 {
			sum /= norm
		}
		dst[i] = clamp16(sum)
	}

	return fromSamples(dst, rate)
}

func sinc(x float64) float64 {
	if x == 0 {
		return 1
	}
	px := math.Pi * x
	return math.Sin(px) / px
}

// blackman over [-1, 1], zero outside.
func blackman(t float64) float64 {
	if t < -1 || t > 1 {
		return 0
	}
	return 0.42 + 0.5*math.Cos(math.Pi*t) + 0.08*math.Cos(2*math.Pi*t)
}

func clamp16(v float64) int16 {
	switch {
	case v > math.MaxInt16:
		return math.MaxInt16
	case v < math.MinInt16:
		return math.MinInt16
	}
	return int16(math.Round(v))
}

// Normalise brings a clip to TargetDBFS RMS, backing off if that would push
// the peak above the ceiling.
//
// Peak-limited rather than peak-normalised: normalising to peak makes a clip
// with one loud consonant quiet throughout, which is exactly the failure that
// makes one prompt in a pack sound wrong next to the others.
func Normalise(a Audio) Audio {
	s := a.samples()
	if len(s) == 0 {
		return a
	}

	var sumSquares float64
	peak := 0.0
	for _, v := range s {
		f := float64(v)
		sumSquares += f * f
		if abs := math.Abs(f); abs > peak {
			peak = abs
		}
	}
	rms := math.Sqrt(sumSquares / float64(len(s)))
	if rms == 0 || peak == 0 {
		return a // digital silence has no level to correct
	}

	full := float64(math.MaxInt16)
	gain := (full * dbToLinear(TargetDBFS)) / rms

	// Do not let the loudest sample clip.
	if ceiling := full * dbToLinear(peakCeilingDBFS); peak*gain > ceiling {
		gain = ceiling / peak
	}

	out := make([]int16, len(s))
	for i, v := range s {
		out[i] = clamp16(float64(v) * gain)
	}
	return fromSamples(out, a.SampleRate)
}

func dbToLinear(db float64) float64 { return math.Pow(10, db/20) }

// DBFS is the RMS level of a clip, for tests and for `pack build` to report.
func DBFS(a Audio) float64 {
	s := a.samples()
	if len(s) == 0 {
		return math.Inf(-1)
	}
	var sumSquares float64
	for _, v := range s {
		sumSquares += float64(v) * float64(v)
	}
	rms := math.Sqrt(sumSquares / float64(len(s)))
	if rms == 0 {
		return math.Inf(-1)
	}
	return 20 * math.Log10(rms/float64(math.MaxInt16))
}

// EncodeWAV wraps PCM in a RIFF/WAVE container, which is what Asterisk plays.
func EncodeWAV(a Audio) []byte {
	var b bytes.Buffer
	data := a.PCM

	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+len(data)))
	b.WriteString("WAVEfmt ")
	_ = binary.Write(&b, binary.LittleEndian, uint32(16))
	_ = binary.Write(&b, binary.LittleEndian, uint16(1)) // PCM
	_ = binary.Write(&b, binary.LittleEndian, uint16(1)) // mono
	_ = binary.Write(&b, binary.LittleEndian, uint32(a.SampleRate))
	_ = binary.Write(&b, binary.LittleEndian, uint32(a.SampleRate*2)) // byte rate
	_ = binary.Write(&b, binary.LittleEndian, uint16(2))              // block align
	_ = binary.Write(&b, binary.LittleEndian, uint16(16))
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(data)))
	b.Write(data)
	return b.Bytes()
}

// DecodeWAV is decodeWAV, exported for the pack builder and the files backend.
func DecodeWAV(b []byte) (Audio, error) { return decodeWAV(b) }

// Prepare runs the whole shared stage: normalise once at the source rate, then
// resample to each pack rate.
//
// Normalising before resampling rather than after means every rate is derived
// from the same corrected signal, so the 8 kHz and 16 kHz versions of a prompt
// cannot drift apart in level — which would be audible as a jump when Asterisk
// switched between them mid-pack.
func Prepare(a Audio) map[int]Audio {
	level := Normalise(a)
	out := make(map[int]Audio, len(PackRates))
	for _, r := range PackRates {
		out[r] = Resample(level, r)
	}
	return out
}
