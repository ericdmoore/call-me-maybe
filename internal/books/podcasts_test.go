package books

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPodcastOpusPlaybackAndHandsetProgress(t *testing.T) {
	requireAudio(t, "ffmpeg")
	requireAudio(t, "ffprobe")
	requireAudio(t, "espeak-ng")
	spool := t.TempDir()
	root := filepath.Join(spool, "cmm-media-cache/podcasts/library")
	input := filepath.Join(spool, "input.sln16")
	pcm(t, input, 4)
	// Raw recording fixture converted to a self describing source for ingestion.
	wav := filepath.Join(spool, "source.wav")
	if err := convertTestAudio(input, wav); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(spool, "source.json")
	c := Catalog{Schema: 1, Books: []Book{{ID: "pod-dogs", Title: "Dogs", Origin: "podcast", Language: "en", Chapters: []Chapter{{Title: "Dogs", Audio: wav}}}}}
	if err := writeJSON(source, c); err != nil {
		t.Fatal(err)
	}
	if err := Prepare(context.Background(), source, root); err != nil {
		t.Fatal(err)
	}
	c, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	b := c.Books[0]
	state := filepath.Join(spool, "progress")
	_ = os.MkdirAll(state, 0700)
	m := &menu{handset: "kitchen", state: state}
	if err = m.saveProgress(b, Position{Started: true, Seconds: 2, Speed: 10}); err != nil {
		t.Fatal(err)
	}
	m.handset = "bedroom"
	if m.loadProgress(b).Started {
		t.Fatal("shared cache shared bookmarks")
	}
	m.handset = "kitchen"
	if m.loadProgress(b).Seconds != 2 {
		t.Fatal("bookmark lost")
	}
	stream, err := startStream(context.Background(), state, filepath.Join(bookDir(root, b), b.Chapters[0].Audio), 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	var samples int64
	for w := range stream.windows {
		if w.err != nil {
			t.Fatal(w.err)
		}
		samples += w.samples
		_ = os.Remove(w.path)
	}
	stream.close()
	if samples < 2*sampleRate || samples > 4*sampleRate {
		t.Fatal("Opus playback seek failed", samples)
	}
	// Exercise the real AGI entry: endpoint marker, scoped root, episode pick,
	// start from beginning, first PCM window and exit, without an Asterisk dependency.
	played := false
	selected := false
	a, tr := fakeAGI(t, func(cmd string) string {
		switch {
		case strings.Contains(cmd, "CHANNEL(endpoint)"):
			return "200 result=1 (kitchen)"
		case strings.Contains(cmd, "CMM_BOOKS_HANDSET"):
			return "200 result=1 (kitchen)"
		case strings.Contains(cmd, "ASTSPOOLDIR"):
			return fmt.Sprintf("200 result=1 (%s)", spool)
		case strings.Contains(cmd, "/last\""):
			if selected {
				return result('0')
			}
			selected = true
			return result('1')
		case strings.Contains(cmd, "/book\""):
			if played {
				return result('0')
			}
			return result('2')
		case strings.Contains(cmd, "/play-"):
			played = true
			return result('0')
		}
		return result(0)
	})
	if err = RunPodcasts(context.Background(), a, func(h string) bool { return h == "kitchen" }); err != nil || !played {
		t.Fatal("scoped podcast AGI did not play", err, tr.text())
	}
	a, tr = fakeAGI(t, func(cmd string) string {
		switch {
		case strings.Contains(cmd, "CHANNEL(endpoint)"), strings.Contains(cmd, "CMM_BOOKS_HANDSET"):
			return "200 result=1 (other)"
		case strings.Contains(cmd, "ASTSPOOLDIR"):
			return fmt.Sprintf("200 result=1 (%s)", spool)
		}
		return result(0)
	})
	if err = RunPodcasts(context.Background(), a, func(string) bool { return false }); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(tr.text(), root) {
		t.Fatal("unsubscribed handset saw catalog")
	}
}

func convertTestAudio(input, output string) error {
	return exec.Command("ffmpeg", "-nostdin", "-v", "error", "-f", "s16le", "-ar", "16000", "-ac", "1", "-i", input, "-y", output).Run()
}
