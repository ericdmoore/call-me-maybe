package books

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type transcript struct {
	mu       sync.Mutex
	commands []string
}

func (tr *transcript) text() string {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return strings.Join(tr.commands, "\n")
}

func fakeAGI(t *testing.T, reply func(string) string) (*AGI, *transcript) {
	t.Helper()
	in, serverOut := io.Pipe()
	serverIn, out := io.Pipe()
	tr := &transcript{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer serverOut.Close()
		_, _ = fmt.Fprint(serverOut, "agi_network: no\n\n")
		s := bufio.NewScanner(serverIn)
		for n := 0; s.Scan() && n < 500; n++ {
			cmd := s.Text()
			tr.mu.Lock()
			tr.commands = append(tr.commands, cmd)
			tr.mu.Unlock()
			if _, err := fmt.Fprintln(serverOut, reply(cmd)); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { _ = out.Close(); _ = in.Close(); _ = serverIn.Close(); <-done })
	a, err := Connect(in, out)
	if err != nil {
		t.Fatal(err)
	}
	return a, tr
}

func testMenu(t *testing.T, reply func(string) string) (*menu, *transcript) {
	t.Helper()
	a, tr := fakeAGI(t, reply)
	return &menu{a: a, root: t.TempDir(), state: t.TempDir(), handset: "test-phone", ctx: context.Background()}, tr
}

func result(key int) string { return fmt.Sprintf("200 result=%d", key) }

func TestPaginationAndBargeIn(t *testing.T) {
	for _, n := range []int{0, 1, 9, 10, 18, 24} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			titles := make([]string, n)
			for i := range titles {
				titles[i] = fmt.Sprintf("title-%02d", i)
			}
			pages := (n + 8) / 9
			announced := 0
			m, tr := testMenu(t, func(cmd string) string {
				if strings.Contains(cmd, "/books\"") {
					announced++
				}
				if strings.Contains(cmd, "/next\"") {
					return result('#')
				}
				if strings.Contains(cmd, "/last\"") {
					return result('1' + (n-1)%9)
				}
				if strings.Contains(cmd, "/empty\"") {
					return result('0')
				}
				return result(0)
			})
			page := 0
			got, err := m.browse("books", titles, &page)
			if err != nil || got != n-1 || announced != pages {
				t.Fatalf("selection=%d pages=%d err=%v", got, announced, err)
			}
			if n > 0 && page != pages-1 {
				t.Fatal("back should retain page")
			}
			if n == 24 && strings.Contains(tr.text(), "title-24") {
				t.Fatal("announced absent title")
			}
		})
	}
	// A child can choose during the heading; do not require hearing the list.
	m, tr := testMenu(t, func(string) string { return result('2') })
	page := 0
	got, err := m.browse("books", []string{"first", "second"}, &page)
	if err != nil || got != 1 || strings.Contains(tr.text(), "STREAM FILE \"first\"") {
		t.Fatal("barge-in failed")
	}
}

func TestLastPagePickerRepeatBackAndInvalid(t *testing.T) {
	titles := make([]string, 24)
	for i := range titles {
		titles[i] = fmt.Sprint(i)
	}
	// Page 3 -> picker -> repeat -> page 2 -> pick its third item.
	keys := []int{'#', '*', '2', '#', '3'}
	m, _ := testMenu(t, func(cmd string) string {
		if strings.Contains(cmd, "/books\"") || strings.Contains(cmd, "/pick-page\"") || strings.HasPrefix(cmd, "WAIT FOR DIGIT") {
			k := keys[0]
			keys = keys[1:]
			return result(k)
		}
		return result(0)
	})
	page := 2
	got, err := m.browse("books", titles, &page)
	if err != nil || got != 11 || page != 1 || len(keys) != 0 {
		t.Fatalf("got %d page %d: %v", got, page, err)
	}
	// Picker zero returns to its originating last page; invalid 9 must not
	// select a nonexistent entry on that page, and star repeats the same page.
	keys = []int{'#', '0', '9', '*', '6'}
	m, _ = testMenu(t, func(cmd string) string {
		if strings.Contains(cmd, "/books\"") || strings.Contains(cmd, "/pick-page\"") {
			k := keys[0]
			keys = keys[1:]
			return result(k)
		}
		return result(0)
	})
	page = 2
	got, err = m.browse("books", titles, &page)
	if err != nil || got != 23 || page != 2 {
		t.Fatalf("got %d page %d: %v", got, page, err)
	}
	m, _ = testMenu(t, func(string) string { return result(0) })
	page = 0
	got, err = m.browse("books", titles, &page)
	if err != nil || got != -1 {
		t.Fatal("idle browse must exit")
	}
	if got, err = m.pickPage(3); err != nil || got != -1 {
		t.Fatal("idle picker must return")
	}
}

func TestMultiDigitPageAndInvalidPicker(t *testing.T) {
	keys := []int{'9', '#', '1', '2', '#'}
	m, _ := testMenu(t, func(cmd string) string {
		if strings.Contains(cmd, "/pick-page\"") || strings.HasPrefix(cmd, "WAIT FOR DIGIT") {
			k := keys[0]
			keys = keys[1:]
			return result(k)
		}
		return result(0)
	})
	// First input 9 is valid at 12 pages; use an overflow instead.
	keys = []int{'9', '9', '#', '1', '2', '#'}
	if got, err := m.pickPage(12); err != nil || got != 11 {
		t.Fatalf("page=%d %v", got, err)
	}
}

func TestAGIProtocolAndAuthentication(t *testing.T) {
	for _, input := range []string{"", strings.Repeat("x\n", 101)} {
		if _, err := Connect(strings.NewReader(input), io.Discard); err == nil {
			t.Fatal("bad handshake accepted")
		}
	}
	for _, line := range []string{"HANGUP", "510 invalid", "200 result=x", "200 result="} {
		a := &AGI{in: bufio.NewScanner(strings.NewReader(line + "\n")), out: io.Discard}
		if _, err := a.command("WAIT FOR DIGIT 1"); err == nil {
			t.Fatal("invalid response accepted")
		}
	}
	a := &AGI{in: bufio.NewScanner(strings.NewReader("200 result=-1 endpos=16000\n")), out: io.Discard}
	if r, err := a.command("STREAM FILE x"); !errors.Is(err, ErrHangup) || r.samples != 16000 {
		t.Fatal("hangup lost offset")
	}
	if _, err := a.command("bad\ncommand"); err == nil {
		t.Fatal("command injection")
	}
	for _, marker := range []string{"", "trunk", "wrong-phone"} {
		a, tr := fakeAGI(t, func(cmd string) string {
			if strings.Contains(cmd, "CHANNEL(endpoint)") {
				return "200 result=1 (test-phone)"
			}
			return "200 result=1 (" + marker + ")"
		})
		if Run(context.Background(), a) == nil || strings.Contains(tr.text(), "ASTSPOOLDIR") {
			t.Fatal("untrusted endpoint reached storage")
		}
	}
}

func fixtureBook(t *testing.T, root string, seconds float64) Book {
	t.Helper()
	b := Book{ID: "sample", Title: "Sample", Language: "en", Origin: "cli", Revision: strings.Repeat("a", 64), Chapters: []Chapter{{Title: "Chapter one", Audio: "chapter-001.sln16", Seconds: seconds}}}
	dir := bookDir(root, b)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	pcm(t, filepath.Join(dir, b.Chapters[0].Audio), seconds)
	if err := writeJSON(filepath.Join(root, "catalog.json"), Catalog{Schema: 1, Books: []Book{b}}); err != nil {
		t.Fatal(err)
	}
	return b
}

func pcm(t *testing.T, path string, seconds float64) {
	t.Helper()
	data := make([]byte, int(seconds*sampleRate)*2)
	for i := 0; i < len(data)/2; i++ {
		binary.LittleEndian.PutUint16(data[i*2:], uint16(int16(6000*math.Sin(float64(i)*2*math.Pi*440/sampleRate))))
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadinessAndEditionScopedBookmarks(t *testing.T) {
	m, _ := testMenu(t, func(string) string { return result(0) })
	b := fixtureBook(t, m.root, 1)
	if !ready(m.root, b) {
		t.Fatal("prepared book not ready")
	}
	p := Position{Chapter: 0, Seconds: 0.5, Speed: 15, Started: true}
	if err := writeJSON(m.progressPath(b), p); err != nil {
		t.Fatal(err)
	}
	if got := m.loadProgress(b); got != p {
		t.Fatal(got)
	}
	other := b
	other.Language = "es"
	if m.loadProgress(other).Started {
		t.Fatal("language crossed")
	}
	other = b
	other.Revision = strings.Repeat("b", 64)
	if m.loadProgress(other).Started {
		t.Fatal("edition crossed")
	}
	m.handset = "other-phone"
	if m.loadProgress(b).Started {
		t.Fatal("handset crossed")
	}
	m.handset = "test-phone"
	p.Speed = 100
	_ = writeJSON(m.progressPath(b), p)
	if m.loadProgress(b).Speed != 10 {
		t.Fatal("corrupt speed trusted")
	}
	_ = writeJSON(filepath.Join(m.root, "catalog.json"), Catalog{Schema: 1})
	if ready(m.root, b) {
		t.Fatal("removed book remains selectable")
	}
	_ = writeJSON(filepath.Join(m.root, "catalog.json"), Catalog{Schema: 1, Books: []Book{b}})
	_ = os.Remove(filepath.Join(bookDir(m.root, b), b.Chapters[0].Audio))
	if ready(m.root, b) {
		t.Fatal("missing audio remains selectable")
	}
}

func requireAudio(t *testing.T, tools ...string) {
	t.Helper()
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " required for audio integration")
		}
	}
}

func TestStreamTempoAndBoundedCleanup(t *testing.T) {
	requireAudio(t, "ffmpeg")
	root := t.TempDir()
	input := filepath.Join(root, "source.sln16")
	pcm(t, input, 95)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := startStream(ctx, root, input, 2, 15)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	var samples int64
	for w := range s.windows {
		if w.err != nil {
			t.Fatal(w.err)
		}
		samples += w.samples
		files, _ := os.ReadDir(s.dir)
		if len(files) > 2 {
			t.Fatalf("unbounded cache: %d", len(files))
		}
		_ = os.Remove(w.path)
	}
	seconds := float64(samples) / sampleRate
	if math.Abs(seconds-62) > 0.15 {
		t.Fatalf("tempo duration %.3f", seconds)
	}
	s.close()
	if _, err := os.Stat(s.dir); !os.IsNotExist(err) {
		t.Fatal("buffer leaked")
	}
	s, err = startStream(ctx, root, input, 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	<-s.windows // leave prefetch blocked: cancellation must join and clean it.
	s.close()
	if _, err := os.Stat(s.dir); !os.IsNotExist(err) {
		t.Fatal("cancel leaked buffer")
	}
}

func TestPlayerControlsPreserveSourcePosition(t *testing.T) {
	requireAudio(t, "ffmpeg")
	plays, pauses := 0, 0
	m, _ := testMenu(t, func(cmd string) string {
		if strings.Contains(cmd, "/play-") {
			plays++
			keys := []int{'2', '6', '5', '0'}
			samples := []int{16000, 32000, 16000, 8000}
			return fmt.Sprintf("200 result=%d endpos=%d", keys[plays-1], samples[plays-1])
		}
		if strings.Contains(cmd, "/paused\"") {
			pauses++
			if pauses == 1 {
				return result('8')
			}
			return result('5')
		}
		return result(0)
	})
	b := fixtureBook(t, m.root, 95)
	p := Position{Speed: 10}
	if err := m.listen(b, &p); err != nil {
		t.Fatal(err)
	}
	if plays != 4 || p.Speed != 10 || math.Abs(p.Seconds-14.8) > 0.001 {
		t.Fatalf("plays=%d position=%+v", plays, p)
	}
	if got := m.loadProgress(b); got != p {
		t.Fatalf("resume=%+v wanted=%+v", got, p)
	}
	files, _ := filepath.Glob(filepath.Join(m.state, "play-*"))
	if len(files) != 0 {
		t.Fatal("player leaked buffers")
	}
}

func TestPlayerHangupAndContinuousChapters(t *testing.T) {
	requireAudio(t, "ffmpeg")
	m, _ := testMenu(t, func(cmd string) string {
		if strings.Contains(cmd, "/play-") {
			return "200 result=-1 endpos=1600"
		}
		return result(0)
	})
	b := fixtureBook(t, m.root, 1)
	p := Position{Speed: 15}
	if err := m.listen(b, &p); !errors.Is(err, ErrHangup) {
		t.Fatal(err)
	}
	if math.Abs(m.loadProgress(b).Seconds-0.15) > 0.001 {
		t.Fatal("hangup lost source position")
	}
	plays := 0
	m, _ = testMenu(t, func(cmd string) string {
		if strings.Contains(cmd, "/play-") {
			plays++
			return "200 result=0 endpos=16000"
		}
		return result(0)
	})
	b = fixtureBook(t, m.root, 0.2)
	b.Chapters = append(b.Chapters, Chapter{Title: "Second", Audio: "chapter-002.sln16", Seconds: 0.2})
	pcm(t, filepath.Join(bookDir(m.root, b), "chapter-002.sln16"), 0.2)
	p = Position{Speed: 10}
	if err := m.listen(b, &p); err != nil {
		t.Fatal(err)
	}
	if plays != 2 || p.Chapter != 1 || p.Seconds != 0.2 {
		t.Fatalf("not continuous: %+v plays=%d", p, plays)
	}
}

func TestPrepareRealAudioAndAtomicFailure(t *testing.T) {
	requireAudio(t, "ffmpeg", "espeak-ng")
	root := t.TempDir()
	source := filepath.Join(root, "input.json")
	library := filepath.Join(root, "library")
	raw := filepath.Join(root, "raw.sln16")
	pcm(t, raw, 0.1)
	wav := filepath.Join(root, "recording.wav")
	if err := exec.Command("ffmpeg", "-v", "error", "-f", "s16le", "-ar", "16000", "-ac", "1", "-i", raw, wav).Run(); err != nil {
		t.Fatal(err)
	}
	c := Catalog{Schema: 1, Books: []Book{{ID: "sample", Title: "A Sample", Language: "en", Origin: "cli", Preview: true, Chapters: []Chapter{{Title: "Opening", Audio: "recording.wav"}}}}}
	_ = writeJSON(source, c)
	if err := Prepare(context.Background(), source, library); err != nil {
		t.Fatal(err)
	}
	prepared, err := Load(library)
	if err != nil {
		t.Fatal(err)
	}
	if !ready(library, prepared.Books[0]) || prepared.Books[0].Chapters[0].Seconds != 0.1 {
		t.Fatal("bad prepared audio")
	}
	if err := Prepare(context.Background(), source, library); err != nil {
		t.Fatal("idempotent import:", err)
	}
	c.Books[0].Chapters[0].Audio = "https://invalid.example/audio.mp3"
	_ = writeJSON(source, c)
	if Prepare(context.Background(), source, library) == nil {
		t.Fatal("remote source accepted")
	}
	after, err := Load(library)
	if err != nil || after.Books[0].Revision != prepared.Books[0].Revision {
		t.Fatal("failed preparation replaced live catalog")
	}
	for _, entry := range []string{"../escape", strings.Repeat("x", 65), "quote\""} {
		c.Books[0].ID = entry
		if validate(c, false) == nil {
			t.Fatal("unsafe ID accepted")
		}
	}
}

func TestRunBookMenuAndChapterSelection(t *testing.T) {
	requireAudio(t, "ffmpeg")
	spool := t.TempDir()
	root := filepath.Join(spool, "cmm-books", "library")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	b := fixtureBook(t, root, 0.1)
	bookHeadings, bookMenus, chapterMenus, played := 0, 0, 0, 0
	a, _ := fakeAGI(t, func(cmd string) string {
		switch {
		case strings.Contains(cmd, "CHANNEL(endpoint)"), strings.Contains(cmd, "CMM_BOOKS_HANDSET"):
			return "200 result=1 (test-phone)"
		case strings.Contains(cmd, "ASTSPOOLDIR"):
			return "200 result=1 (" + spool + ")"
		case strings.Contains(cmd, "/books\""):
			bookHeadings++
			if bookHeadings == 1 {
				return result('1')
			}
			return result('0')
		case strings.Contains(cmd, "/book\""):
			bookMenus++
			if bookMenus == 1 {
				return result('3')
			}
			return result('0')
		case strings.Contains(cmd, "/chapters\""):
			chapterMenus++
			return result('1')
		case strings.Contains(cmd, "/play-"):
			played++
			return "200 result=0 endpos=1600"
		}
		return result(0)
	})
	if err := Run(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if bookHeadings != 2 || bookMenus != 2 || chapterMenus != 1 || played != 1 {
		t.Fatalf("menus=%d,%d,%d plays=%d", bookHeadings, bookMenus, chapterMenus, played)
	}
	m := menu{state: filepath.Join(spool, "cmm-books", "progress"), handset: "test-phone"}
	if p := m.loadProgress(b); !p.Started || p.Seconds != 0.1 {
		t.Fatal(p)
	}
}

func TestBookResumeStartAndBackDoNotResetSpeed(t *testing.T) {
	requireAudio(t, "ffmpeg")
	for _, selection := range []int{'1', '2'} {
		bookMenus, plays := 0, 0
		m, _ := testMenu(t, func(cmd string) string {
			if strings.Contains(cmd, "/resume\"") {
				bookMenus++
				if bookMenus == 1 {
					return result(selection)
				}
				return result('0')
			}
			if strings.Contains(cmd, "/play-") {
				plays++
				return "200 result=48 endpos=1600"
			}
			return result(0)
		})
		b := fixtureBook(t, m.root, 2)
		p := Position{Speed: 15, Seconds: 1, Started: true}
		_ = writeJSON(m.progressPath(b), p)
		if err := m.book(b); err != nil {
			t.Fatal(err)
		}
		want := 0.15
		if selection == '1' {
			want += 1
		}
		if got := m.loadProgress(b); got.Speed != 15 || math.Abs(got.Seconds-want) > 0.001 || plays != 1 {
			t.Fatalf("selection=%c got=%+v plays=%d", selection, got, plays)
		}
	}
}

func TestPlayerChapterKeysPauseTimeoutAndConversionFailure(t *testing.T) {
	requireAudio(t, "ffmpeg")
	plays := 0
	m, _ := testMenu(t, func(cmd string) string {
		if strings.Contains(cmd, "/play-") {
			plays++
			keys := []int{'3', '1', '5'}
			return fmt.Sprintf("200 result=%d endpos=1600", keys[plays-1])
		}
		return result(0)
	})
	b := fixtureBook(t, m.root, 2)
	b.Chapters = append(b.Chapters, Chapter{Title: "Two", Audio: "chapter-002.sln16", Seconds: 2})
	pcm(t, filepath.Join(bookDir(m.root, b), "chapter-002.sln16"), 2)
	p := Position{Speed: 10}
	if err := m.listen(b, &p); err != nil {
		t.Fatal(err)
	}
	if p.Chapter != 0 || p.Seconds != 0.1 {
		t.Fatal(p)
	}
	m, _ = testMenu(t, func(string) string { return result(0) })
	b = fixtureBook(t, m.root, 1)
	_ = os.Remove(filepath.Join(bookDir(m.root, b), b.Chapters[0].Audio))
	p = Position{Speed: 10}
	if m.listen(b, &p) == nil {
		t.Fatal("missing source played")
	}
	files, _ := filepath.Glob(filepath.Join(m.state, "play-*"))
	if len(files) != 0 {
		t.Fatal("failure leaked buffers")
	}
}
