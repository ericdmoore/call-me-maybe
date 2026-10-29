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
	// Ogg stream serial numbers must not create another edition when a job
	// retries after conversion but before publishing its complete catalog.
	if err := Prepare(context.Background(), source, root); err != nil {
		t.Fatal(err)
	}
	again, err := Load(root)
	if err != nil || again.Books[0].Revision != b.Revision {
		t.Fatal("retry created a duplicate Opus edition", err)
	}
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

func TestGenericPodcastProgressAndReapLease(t *testing.T) {
	requireAudio(t, "ffmpeg")
	requireAudio(t, "ffprobe")
	requireAudio(t, "espeak-ng")
	dir := t.TempDir()
	root := filepath.Join(dir, "cmm-media-cache/podcasts/library")
	input := filepath.Join(dir, "input.sln16")
	pcm(t, input, 2)
	wav := filepath.Join(dir, "audio.wav")
	if err := convertTestAudio(input, wav); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "source.json")
	c := Catalog{Schema: 1, Books: []Book{{ID: "pod-generic", Feed: "feed-0123456789abcdef0123456789abcdef", Title: "Nature: Birds", Language: "en", Origin: "podcast", Chapters: []Chapter{{Title: "Birds", Audio: wav}}}}}
	other := c.Books[0]
	other.ID = "pod-other"
	other.Feed = "feed-abcdef0123456789abcdef0123456789"
	c.Books = append(c.Books, other)
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
	for _, feed := range []string{b.Feed, other.Feed} {
		a, tr := fakeAGI(t, func(command string) string {
			switch {
			case strings.Contains(command, "CHANNEL(endpoint)"), strings.Contains(command, "CMM_BOOKS_HANDSET"):
				return "200 result=1 (kitchen)"
			case strings.Contains(command, "ASTSPOOLDIR"):
				return fmt.Sprintf("200 result=1 (%s)", dir)
			case strings.Contains(command, "/last\""):
				return result('0')
			}
			return result(0)
		})
		if err = RunPodcastFeeds(context.Background(), a, func(_ string, id string) bool { return id == feed }); err != nil {
			t.Fatal(err)
		}
		expected, hidden := "pod-generic-", "pod-other-"
		if feed == other.Feed {
			expected, hidden = hidden, expected
		}
		if !strings.Contains(tr.text(), expected) || strings.Contains(tr.text(), hidden) {
			t.Fatal("feed authorization leaked catalog", tr.text())
		}
	}
	state := filepath.Join(dir, "progress")
	_ = os.MkdirAll(state, 0700)
	kitchen := &menu{handset: "kitchen", state: state}
	bedroom := &menu{handset: "bedroom", state: state}
	if err = kitchen.saveProgress(b, Position{Speed: 10, Seconds: 1, Started: true}); err != nil {
		t.Fatal(err)
	}
	if bedroom.loadProgress(b).Started || kitchen.loadProgress(b).Seconds != 1 {
		t.Fatal("handset bookmarks conflated")
	}
	release, err := podcastPlaybackLock(root, false)
	if err != nil {
		t.Fatal(err)
	}
	// Retire the edition while a menu still references its immutable file paths.
	if err = publishJSON(filepath.Join(root, "catalog.json"), Catalog{Schema: 1, Books: []Book{}}, 0644); err != nil {
		t.Fatal(err)
	}
	if err = ReapPodcasts(root); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(bookDir(root, b)); err != nil {
		t.Fatal("active artifact reaped", err)
	}
	release()
	if err = ReapPodcasts(root); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(bookDir(root, b)); !os.IsNotExist(err) {
		t.Fatal("unreferenced artifact retained")
	}
	if kitchen.loadProgress(b).Seconds != 1 {
		t.Fatal("eviction deleted bookmark")
	}
}
