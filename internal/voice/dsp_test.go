package voice

import (
	"math"
	"testing"
)

// tone builds a sine wave: the only input where you can say exactly what
// correct output looks like.
func tone(freq float64, rate, samples int, amp float64) Audio {
	s := make([]int16, samples)
	for i := range s {
		s[i] = int16(amp * math.MaxInt16 * math.Sin(2*math.Pi*freq*float64(i)/float64(rate)))
	}
	return fromSamples(s, rate)
}

// energyAt measures how much of a given frequency is present, by correlating
// against that frequency (a one-bin Goertzel).
func energyAt(a Audio, freq float64) float64 {
	s := a.samples()
	var re, im float64
	for i, v := range s {
		th := 2 * math.Pi * freq * float64(i) / float64(a.SampleRate)
		re += float64(v) * math.Cos(th)
		im += float64(v) * math.Sin(th)
	}
	return math.Hypot(re, im) / float64(len(s))
}

func TestResampleKeepsDurationAndRate(t *testing.T) {
	in := tone(440, 22050, 22050, 0.5) // one second
	for _, rate := range PackRates {
		out := Resample(in, rate)
		if out.SampleRate != rate {
			t.Errorf("rate = %d, want %d", out.SampleRate, rate)
		}
		if d := out.Duration(); math.Abs(d.Seconds()-1) > 0.01 {
			t.Errorf("%d Hz: duration = %v, want ~1s", rate, d)
		}
	}
}

// A tone that survives the trip is the basic correctness check.
func TestResamplePreservesAToneInBand(t *testing.T) {
	in := tone(1000, 22050, 22050, 0.5)
	out := Resample(in, 8000)

	atSignal := energyAt(out, 1000)
	atOther := energyAt(out, 2500)
	if atSignal < 4*atOther {
		t.Errorf("1 kHz did not survive downsampling: signal %.0f vs elsewhere %.0f", atSignal, atOther)
	}
}

// The reason for band-limiting rather than linear interpolation. A 6 kHz tone
// is above the 4 kHz Nyquist of an 8 kHz stream: it must be *attenuated*, not
// folded back to 2 kHz. Naive decimation produces a loud 2 kHz whistle, which
// on speech is the metallic lisp people call "the cheap one".
func TestResampleDoesNotAlias(t *testing.T) {
	in := tone(6000, 22050, 22050, 0.5)
	out := Resample(in, 8000)

	// 6000 Hz folds to |8000 - 6000| = 2000 Hz.
	aliased := energyAt(out, 2000)
	reference := energyAt(in, 6000)

	if aliased > reference/10 {
		t.Errorf("6 kHz aliased into 2 kHz at %.0f (source tone was %.0f) — "+
			"the anti-alias filter is not working", aliased, reference)
	}
}

func TestResampleIsANoOpAtTheSameRate(t *testing.T) {
	in := tone(440, 8000, 800, 0.5)
	out := Resample(in, 8000)
	if string(out.PCM) != string(in.PCM) {
		t.Error("resampling to the same rate should change nothing")
	}
}

func TestResampleHandlesTinyInput(t *testing.T) {
	for _, n := range []int{0, 1, 2} {
		in := fromSamples(make([]int16, n), 22050)
		out := Resample(in, 8000)
		if out.SampleRate != 8000 && n > 1 {
			t.Errorf("%d samples: rate = %d", n, out.SampleRate)
		}
	}
}

// ── loudness ────────────────────────────────────────────────────────────

func TestNormaliseHitsTheTarget(t *testing.T) {
	for _, amp := range []float64{0.02, 0.1, 0.5} {
		out := Normalise(tone(440, 16000, 16000, amp))
		if got := DBFS(out); math.Abs(got-TargetDBFS) > 0.5 {
			t.Errorf("amp %.2f: level = %.2f dBFS, want %.0f", amp, got, TargetDBFS)
		}
	}
}

// Two clips recorded at wildly different levels must come out matched. This is
// the property that stops one prompt in a pack being inaudible next to another.
func TestNormaliseMatchesClipsToEachOther(t *testing.T) {
	quiet := Normalise(tone(440, 16000, 16000, 0.03))
	loud := Normalise(tone(440, 16000, 16000, 0.8))

	if d := math.Abs(DBFS(quiet) - DBFS(loud)); d > 0.5 {
		t.Errorf("clips differ by %.2f dB after normalising", d)
	}
}

// A sample that clips is a click on every call forever.
func TestNormaliseNeverClips(t *testing.T) {
	// A signal with a high peak relative to its RMS: mostly quiet with one
	// loud spike, which is what naive RMS gain would push over the top.
	s := make([]int16, 8000)
	for i := range s {
		s[i] = int16(0.01 * math.MaxInt16 * math.Sin(2*math.Pi*440*float64(i)/8000))
	}
	s[4000] = math.MaxInt16 - 1

	out := Normalise(fromSamples(s, 8000)).samples()
	for i, v := range out {
		if v == math.MaxInt16 || v == math.MinInt16 {
			t.Fatalf("sample %d hit full scale — normalisation clipped", i)
		}
	}
}

func TestNormaliseLeavesSilenceAlone(t *testing.T) {
	in := fromSamples(make([]int16, 800), 8000)
	out := Normalise(in)
	if string(out.PCM) != string(in.PCM) {
		t.Error("digital silence has no level to correct and must not be amplified")
	}
}

// ── WAV ─────────────────────────────────────────────────────────────────

func TestWAVRoundTrip(t *testing.T) {
	for _, rate := range PackRates {
		in := tone(440, rate, rate/2, 0.4)
		got, err := DecodeWAV(EncodeWAV(in))
		if err != nil {
			t.Fatalf("%d Hz: %v", rate, err)
		}
		if got.SampleRate != in.SampleRate || string(got.PCM) != string(in.PCM) {
			t.Errorf("%d Hz: round trip changed the audio", rate)
		}
	}
}

// ── the whole shared stage ──────────────────────────────────────────────

func TestPrepareProducesBothPackRatesAtOneLevel(t *testing.T) {
	out := Prepare(tone(440, 24000, 24000, 0.05))

	if len(out) != len(PackRates) {
		t.Fatalf("got %d rates, want %d", len(out), len(PackRates))
	}
	for _, rate := range PackRates {
		a, ok := out[rate]
		if !ok {
			t.Fatalf("no %d Hz output", rate)
		}
		if a.SampleRate != rate {
			t.Errorf("rate = %d, want %d", a.SampleRate, rate)
		}
	}

	// Normalising happens once before resampling, so the two rates cannot
	// drift apart in level — which would be an audible jump when Asterisk
	// switched between them.
	if d := math.Abs(DBFS(out[8000]) - DBFS(out[16000])); d > 0.5 {
		t.Errorf("8 kHz and 16 kHz differ by %.2f dB", d)
	}
	if got := DBFS(out[16000]); math.Abs(got-TargetDBFS) > 1.0 {
		t.Errorf("level = %.2f dBFS, want %.0f", got, TargetDBFS)
	}
}
