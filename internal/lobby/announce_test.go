package lobby

import (
	"io"
	"log/slog"
	"testing"
	"time"
)

type announceHarness struct {
	fake     *fakeARI
	ann      *Announcement
	finished chan struct{}
}

func startAnnouncement(t *testing.T, media []string, tweak ...func(*fakeARI)) *announceHarness {
	t.Helper()
	h := &announceHarness{fake: newFakeARI(), finished: make(chan struct{})}
	for _, tw := range tweak {
		tw(h.fake)
	}
	h.ann = NewAnnouncement("ch-kitchen-9", media, AnnounceDeps{
		ARI:        h.fake,
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		OnFinished: func(*Announcement) { close(h.finished) },
	})
	go h.ann.Run()
	return h
}

func (h *announceHarness) waitFinished(t *testing.T) {
	t.Helper()
	select {
	case <-h.finished:
	case <-time.After(2 * time.Second):
		t.Fatal("announcement never finished")
	}
}

func TestAnnouncementSaysEachClipInOrderThenHangsUp(t *testing.T) {
	media, ok := AnnounceMedia(AnnounceKindBalance, "12")
	if !ok {
		t.Fatal("the balance announcement should exist")
	}
	h := startAnnouncement(t, media)

	for _, want := range media {
		c := h.fake.expect(t, "Play")
		if c.Args[1] != want {
			t.Fatalf("played %q, want %q", c.Args[1], want)
		}
		// Nothing is said over the top of a clip still playing: the next
		// Play must wait for this one's PlaybackFinished.
		h.fake.expectNone(t, 30*time.Millisecond)
		h.ann.PlaybackFinished(c.Args[2])
	}
	h.fake.expect(t, "Hangup")
	h.waitFinished(t)
}

func TestAnnouncementMediaIsAClipANumberAndGoodbye(t *testing.T) {
	media, _ := AnnounceMedia(AnnounceKindBalance, "0012")
	want := []string{
		"sound:call-me-maybe/system/balance-low", // the bundled pack, whatever PROMPT_MEDIA_PREFIX says
		"number:12",                              // Asterisk reads it; nothing is synthesised
		sayGoodbye,
	}
	if len(media) != len(want) {
		t.Fatalf("media = %v, want %v", media, want)
	}
	for i := range want {
		if media[i] != want[i] {
			t.Errorf("media[%d] = %q, want %q", i, media[i], want[i])
		}
	}
	for _, bad := range []struct{ kind, value string }{
		{"balance", ""}, {"balance", "12.50"}, {"balance", "-1"},
		{"balance", "12345678"}, {"balance", "twelve"}, {"weather", "12"},
	} {
		if _, ok := AnnounceMedia(bad.kind, bad.value); ok {
			t.Errorf("AnnounceMedia(%q, %q) accepted, want refused", bad.kind, bad.value)
		}
	}
}

func TestAnnouncementHangupMidClipTearsDown(t *testing.T) {
	media, _ := AnnounceMedia(AnnounceKindBalance, "3")
	h := startAnnouncement(t, media)
	h.fake.expect(t, "Play")
	// The handset hangs up during the first clip. Nothing further is said,
	// and cleanup still runs exactly once.
	h.ann.CallerGone()
	if c := h.fake.expect(t, "Hangup"); c.Args[0] != "ch-kitchen-9" {
		t.Fatalf("hung up %q", c.Args[0])
	}
	h.waitFinished(t)
	h.fake.expectNone(t, 30*time.Millisecond)
}

func TestAnnouncementSurvivesAMissingClip(t *testing.T) {
	// A clip Asterisk cannot find fails to start. The documented behaviour
	// everywhere in this package is silence and carrying on, and here that
	// means the number is still read and the call still ends cleanly.
	media, _ := AnnounceMedia(AnnounceKindBalance, "7")
	h := startAnnouncement(t, media, func(f *fakeARI) { f.failPlay.Store(true) })
	for range media {
		h.fake.expect(t, "Play")
	}
	h.fake.expect(t, "Hangup")
	h.waitFinished(t)
}
