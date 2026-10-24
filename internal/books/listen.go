package books

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Listening from outside the house (#155): Dial-a-Story on a public line.
//
// doorman never plays a book. A caller who chooses a shelf on a public line's
// menu is released, the way a caller leaving a message is, into the generated
// [cmm-listen] context (internal/render), which counts them against the
// line's listener cap, sets Asterisk's own absolute timeout, and runs this
// AGI. The menus, the pages, the player and its FFmpeg windows are the ones a
// handset's *2665 uses — one implementation of the books menu, not two.
//
// What differs is what the caller is given, and it arrives in channel
// variables that only doorman sets (a caller on a trunk cannot set one):
//
//	CMM_LISTEN_MODE     "shelf" to browse, "story" for the story of the day
//	CMM_LISTEN_TITLES   the shelf's catalog ids, comma-separated, in order
//	CMM_LISTEN_SECONDS  how long the call may still last
//	CMM_LISTEN_WARNING  how long before that the caller is told so
//
// The list a caller browses is built from CMM_LISTEN_TITLES and nothing else:
// a key selects an index into that list, so a title not on the shelf has no
// key, no page and no path to it. There is no fallback to the catalog — an
// empty or unusable list is refused, never widened.
//
// There is no handset, so there are no bookmarks: a position lasts as long as
// the call. Nothing here reads doorman's configuration, its call log or its
// journal.

// The bundled system phrases (prompts/manifest.json "system"), played by
// path because they are the house's voice, not the books menu's.
const (
	listenWarningClip = "call-me-maybe/system/listen-warning"
	listenEndedClip   = "call-me-maybe/system/listen-ended"
)

// endLead is how long before Asterisk's hard limit the AGI says goodbye, so
// the caller hears one rather than a cut-off.
const endLead = 5 * time.Second

var errTimeUp = errors.New("listening time is up")

// limit is an outside caller's clock: one warning, then the end.
type limit struct {
	now           func() time.Time
	warnAt, endAt time.Time
	warned        bool
}

func newLimit(now func() time.Time, total, warning time.Duration) *limit {
	start := now()
	return &limit{now: now, warnAt: start.Add(total - warning), endAt: start.Add(total - endLead)}
}

// check runs before every AGI command: past the end it refuses the command;
// past the warning it says so once, unskippable, and lets the command run.
func (l *limit) check(a *AGI) error {
	now := l.now()
	if !now.Before(l.endAt) {
		return errTimeUp
	}
	if !l.warned && !now.Before(l.warnAt) {
		l.warned = true
		if _, err := a.command("STREAM FILE " + quote(listenWarningClip) + ` ""`); err != nil {
			return err
		}
	}
	return nil
}

// until is how long until the limit next has something to say.
func (l *limit) until() time.Duration {
	if !l.warned && l.warnAt.Before(l.endAt) {
		return l.warnAt.Sub(l.now())
	}
	return l.endAt.Sub(l.now())
}

// RunListen is the AGI [cmm-listen] runs for an outside caller.
func RunListen(ctx context.Context, a *AGI) error {
	vars := map[string]string{}
	for _, name := range []string{"CMM_LISTEN_MODE", "CMM_LISTEN_TITLES", "CMM_LISTEN_SECONDS", "CMM_LISTEN_WARNING", "ASTSPOOLDIR"} {
		v, err := a.variable("${" + name + "}")
		if err != nil {
			return err
		}
		vars[name] = v
	}
	titles, err := shelfTitles(vars["CMM_LISTEN_TITLES"])
	if err != nil {
		return err
	}
	mode := vars["CMM_LISTEN_MODE"]
	if mode != "shelf" && !(mode == "story" && len(titles) == 1) {
		return errors.New("listening needs a shelf, or a story of exactly one title")
	}
	seconds, err := strconv.Atoi(vars["CMM_LISTEN_SECONDS"])
	if err != nil || seconds <= 0 {
		return errors.New("listening needs a time limit")
	}
	warning, err := strconv.Atoi(vars["CMM_LISTEN_WARNING"])
	if err != nil || warning < 0 {
		return errors.New("listening needs a warning lead")
	}
	spool := vars["ASTSPOOLDIR"]
	if !filepath.IsAbs(spool) || filepath.Clean(spool) != spool || strings.ContainsAny(spool, "\r\n\x00") {
		return errors.New("invalid Asterisk spool")
	}
	return listenIn(ctx, a, filepath.Join(spool, "cmm-books"), mode, titles,
		newLimit(time.Now, time.Duration(seconds)*time.Second, time.Duration(warning)*time.Second))
}

// listenIn is RunListen once its variables are read: separate so a test can
// hand it a clock.
func listenIn(ctx context.Context, a *AGI, books, mode string, titles []string, lim *limit) error {
	m := &menu{a: a, root: filepath.Join(books, "library"), state: filepath.Join(books, "progress"), ctx: ctx,
		public: true, memory: map[string]Position{}, story: mode == "story"}
	if err := os.MkdirAll(m.state, 0o700); err != nil {
		return err
	}
	c, err := Load(m.root)
	if os.IsNotExist(err) {
		_, err = a.play("vm-nomessages")
		return err
	}
	if err != nil {
		return err
	}
	if m.clips, err = loadMenus(m.root, c); err != nil {
		m.clips = nil
		fmt.Fprintln(diagnostics, "books: menu audio unavailable, using compatibility prompts:", err)
	}
	list := shelfOf(c, titles)

	a.lim = lim
	if mode == "story" {
		err = m.storyOfTheDay(list)
	} else {
		err = m.shelf(list)
	}
	if errors.Is(err, errTimeUp) {
		a.lim = nil
		_, err = a.command("STREAM FILE " + quote(listenEndedClip) + ` ""`)
		if errors.Is(err, ErrHangup) {
			return ErrHangup
		}
		return err
	}
	return err
}

// shelfTitles parses CMM_LISTEN_TITLES. Every id must be one the catalog
// could hold; anything else refuses the whole call rather than guessing.
func shelfTitles(v string) ([]string, error) {
	if v == "" {
		return nil, errors.New("listening needs a shelf")
	}
	titles := strings.Split(v, ",")
	if len(titles) > 128 {
		return nil, errors.New("shelf exceeds the catalog limit")
	}
	for _, t := range titles {
		if !idPattern.MatchString(t) {
			return nil, errors.New("invalid shelf title")
		}
	}
	return titles, nil
}

// shelfOf is the shelf as the catalog has it: the books it names, in the
// shelf's order. A title the catalog lacks is left out — said to the Asterisk
// log, never to the caller — so a key can only ever select a listed title.
func shelfOf(c Catalog, titles []string) []Book {
	byID := make(map[string]Book, len(c.Books))
	for _, b := range c.Books {
		byID[b.ID] = b
	}
	var list []Book
	for _, t := range titles {
		if b, ok := byID[t]; ok {
			list = append(list, b)
		} else {
			fmt.Fprintln(diagnostics, "books: shelf title is not in the prepared catalog:", t)
		}
	}
	return list
}

// storyOfTheDay plays its one title from the beginning, then says goodbye.
func (m *menu) storyOfTheDay(list []Book) error {
	if len(list) != 1 || !ready(m.root, list[0]) {
		if _, err := m.prompt("unavailable"); err != nil {
			return err
		}
		_, err := m.prompt("goodbye")
		return err
	}
	p := Position{Speed: 10}
	if err := m.listen(list[0], &p); err != nil {
		return err
	}
	_, err := m.prompt("goodbye")
	return err
}

// playWindow streams one prepared window. With a limit, a window that would
// carry the call past the warning or the end is played in pieces, cut where
// the limit has something to say, so the warning comes on time and the
// goodbye before Asterisk's cut-off; the response's samples are always the
// window's own position.
func (m *menu) playWindow(w window) (response, error) {
	base := strings.TrimSuffix(w.path, ".sln16")
	const keys = ` "0123456789*#"`
	if m.a.lim == nil {
		return m.a.command("STREAM FILE " + quote(base) + keys)
	}
	var data []byte
	var offset int64
	for {
		// Settle anything due now — the warning, or the end — before
		// measuring how far this piece may run.
		lim := m.a.lim
		m.a.lim = nil
		err := lim.check(m.a)
		m.a.lim = lim
		if err != nil {
			return response{samples: offset}, err
		}
		end := w.samples
		if cut := offset + int64(lim.until().Seconds()*sampleRate); cut < end {
			end = max(cut, offset+sampleRate/10)
		}
		if end >= w.samples && offset == 0 {
			return m.a.command("STREAM FILE " + quote(base) + keys)
		}
		if data == nil {
			if data, err = os.ReadFile(w.path); err != nil {
				return response{samples: offset}, err
			}
		}
		end = min(end, int64(len(data)/2))
		piece := base + "-cut"
		if err := os.WriteFile(piece+".sln16", data[offset*2:end*2], 0o600); err != nil {
			return response{samples: offset}, err
		}
		r, err := m.a.command("STREAM FILE " + quote(piece) + keys)
		_ = os.Remove(piece + ".sln16")
		r.samples = offset + min(max(r.samples, 0), end-offset)
		if err != nil || r.key != 0 || end >= w.samples {
			return r, err
		}
		offset = end
	}
}
