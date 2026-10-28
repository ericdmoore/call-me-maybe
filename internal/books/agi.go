package books

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var ErrHangup = errors.New("channel ended")

// stderr reaches the Asterisk log; stdout is the AGI protocol.
var diagnostics io.Writer = os.Stderr

// AGI has no daemon credentials. stdout is reserved for the Asterisk protocol;
// raw responses (which can contain digits) never appear in diagnostics.
type AGI struct {
	in  *bufio.Scanner
	out io.Writer
	// lim is an outside caller's time limit (listen.go), checked before every
	// command so no prompt, wait or recording can carry a call past it. Nil
	// for a household handset, which has none.
	lim *limit
}
type response struct {
	key     int
	samples int64
	value   string
}

func Connect(in io.Reader, out io.Writer) (*AGI, error) {
	a := &AGI{in: bufio.NewScanner(in), out: out}
	for n := 0; n < 100; n++ {
		if !a.in.Scan() {
			return nil, ErrHangup
		}
		if a.in.Text() == "" {
			return a, nil
		}
	}
	return nil, errors.New("invalid AGI handshake")
}

func quote(s string) string { return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"` }

func (a *AGI) command(command string) (response, error) {
	var r response
	if strings.ContainsAny(command, "\r\n\x00") {
		return r, errors.New("invalid AGI command")
	}
	if a.lim != nil {
		// The limit speaks through this same method; unset while it does,
		// so its own warning is not checked against itself.
		lim := a.lim
		a.lim = nil
		err := lim.check(a)
		a.lim = lim
		if err != nil {
			return r, err
		}
	}
	if _, err := fmt.Fprintln(a.out, command); err != nil {
		return r, ErrHangup
	}
	if !a.in.Scan() || a.in.Text() == "HANGUP" {
		return r, ErrHangup
	}
	line := a.in.Text()
	if !strings.HasPrefix(line, "200 result=") {
		return r, errors.New("AGI command failed")
	}
	fields := strings.Fields(strings.TrimPrefix(line, "200 result="))
	if len(fields) == 0 {
		return r, errors.New("invalid AGI response")
	}
	var err error
	r.key, err = strconv.Atoi(fields[0])
	if err != nil {
		return r, errors.New("invalid AGI result")
	}
	for _, field := range fields[1:] {
		if strings.HasPrefix(field, "endpos=") {
			r.samples, _ = strconv.ParseInt(strings.TrimPrefix(field, "endpos="), 10, 64)
		}
	}
	if start := strings.Index(line, " ("); start >= 0 && strings.HasSuffix(line, ")") {
		r.value = line[start+2 : len(line)-1]
	}
	if r.key < 0 {
		return r, ErrHangup
	}
	return r, nil
}

func (a *AGI) variable(expr string) (string, error) {
	r, err := a.command("GET FULL VARIABLE " + quote(expr))
	if err == nil && r.key != 1 {
		err = errors.New("required Asterisk variable missing")
	}
	return r.value, err
}

func (a *AGI) play(path string) (int, error) {
	r, err := a.command("STREAM FILE " + quote(path) + ` "0123456789*#"`)
	return r.key, err
}

func (a *AGI) number(n int) (int, error) {
	r, err := a.command(fmt.Sprintf(`SAY NUMBER %d "0123456789*#"`, n))
	return r.key, err
}

func (a *AGI) wait() (int, error) {
	ms := int64(10000)
	if a.lim != nil {
		// Never wait past the next thing the limit has to say.
		if d := a.lim.until().Milliseconds(); d < ms {
			ms = max(d, 100)
		}
	}
	r, err := a.command(fmt.Sprintf("WAIT FOR DIGIT %d", ms))
	return r.key, err
}

type menu struct {
	a                    *AGI
	root, state, handset string
	ctx                  context.Context
	clips                map[string]string
	// public is an outside caller (listen.go): no handset, so no bookmark
	// survives the call. Positions live in memory, for this call alone.
	allowed func(string) bool
	public  bool
	memory  map[string]Position
	// story plays one title with no browsing and no spoken help first.
	story bool
}

func (m *menu) prompt(name string) (int, error) {
	if clip := m.clips[name]; clip != "" {
		return m.a.play(clip)
	}
	return m.a.play(filepath.Join(m.root, "prompts-v1", name))
}

// Run trusts the authenticated PJSIP endpoint and a renderer-set marker, never
// caller ID. Trunks and Local channels cannot borrow a handset's bookmarks.
func Run(ctx context.Context, a *AGI) error { return runLibrary(ctx, a, "cmm-books", nil) }

// RunPodcasts reuses the offline player, but authorises the handset before
// showing its catalog and at every playback window. No caller ID trust.
func RunPodcasts(ctx context.Context, a *AGI, allowed func(string) bool) error {
	if allowed == nil {
		return errors.New("podcast subscriptions unavailable")
	}
	return runLibrary(ctx, a, "cmm-media-cache/podcasts", allowed)
}
func runLibrary(ctx context.Context, a *AGI, subdir string, allowed func(string) bool) error {
	handset, err := a.variable("${CHANNEL(endpoint)}")
	if err != nil {
		return err
	}
	marker, err := a.variable("${CMM_BOOKS_HANDSET}")
	if err != nil {
		return err
	}
	if handset != marker || !idPattern.MatchString(handset) {
		return errors.New("books require a local handset")
	}
	spool, err := a.variable("${ASTSPOOLDIR}")
	if err != nil {
		return err
	}
	if !filepath.IsAbs(spool) || filepath.Clean(spool) != spool || strings.ContainsAny(spool, "\r\n\x00") {
		return errors.New("invalid Asterisk spool")
	}
	if allowed != nil && !allowed(handset) {
		_, err = a.play("vm-nomessages")
		return err
	}
	m := &menu{a: a, root: filepath.Join(spool, subdir, "library"), state: filepath.Join(spool, subdir, "progress"), handset: handset, ctx: ctx, allowed: allowed}
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
	// A stale or incomplete bundle must not silence Books: the compatibility
	// prompts are always prepared alongside the catalog and number-safe.
	if m.clips, err = loadMenus(m.root, c); err != nil {
		m.clips = nil
		fmt.Fprintln(diagnostics, "books: menu audio unavailable, using compatibility prompts:", err)
	}
	return m.shelf(c.Books)
}

// shelf browses a list of books until the caller backs out. A handset's list
// is the whole catalog; an outside caller's is the shelf its line offers, and
// nothing else — the index a key selects is into this list alone.
func (m *menu) shelf(list []Book) error {
	paths := make([]string, len(list))
	for i, b := range list {
		paths[i] = filepath.Join(bookDir(m.root, b), "title")
	}
	page := 0
	for {
		index, err := m.browse("books", paths, &page)
		if err != nil {
			return err
		}
		if index < 0 {
			_, err = m.prompt("goodbye")
			return err
		}
		if (m.allowed != nil && !m.allowed(m.handset)) || !ready(m.root, list[index]) {
			if _, err := m.prompt("unavailable"); err != nil {
				return err
			}
			continue
		}
		if err := m.book(list[index]); err != nil {
			return err
		}
	}
}

// browse uses one immutable list. Index keys are local to the announced page.
// -1 means back (including three unanswered/invalid attempts).
func (m *menu) browse(kind string, titles []string, savedPage *int) (int, error) {
	page, failures := *savedPage, 0
	repeatArmed := false
	defer func() { *savedPage = page }()
	for failures < 3 {
		key, err := m.announce(kind, titles, page)
		if err != nil {
			return -1, err
		}
		if key == 0 {
			key, err = m.a.wait()
			if err != nil {
				return -1, err
			}
		}
		// Star repeats immediately. Pound as the next input during that repeat
		// opens the picker; any other input (including silence) clears the prefix.
		picker := repeatArmed && key == '#'
		repeatArmed = key == '*'
		switch {
		case key == '0':
			return -1, nil
		case key == '*':
			failures = 0
		case key == '#' && len(titles) > 0:
			pages := (len(titles) + 8) / 9
			if !picker && page+1 < pages {
				page++
				failures = 0
				continue
			}
			chosen, err := m.pickPage(pages)
			if err != nil {
				return -1, err
			}
			if chosen >= 0 {
				page = chosen
			}
		case key >= '1' && key <= '9' && page*9+key-'1' < len(titles):
			return page*9 + key - '1', nil
		default:
			failures++
			if key != 0 {
				if _, err := m.prompt("invalid"); err != nil {
					return -1, err
				}
			}
		}
	}
	return -1, nil
}

func (m *menu) announce(kind string, titles []string, page int) (int, error) {
	if len(titles) == 0 {
		return m.prompt("empty")
	}
	if clip := m.clips[pageKey(m.root, kind, titles, page)]; clip != "" {
		return m.a.play(clip)
	}
	steps := []func() (int, error){func() (int, error) { return m.prompt(kind) }, func() (int, error) { return m.prompt("page") }, func() (int, error) { return m.a.number(page + 1) }, func() (int, error) { return m.prompt("of") }, func() (int, error) { return m.a.number((len(titles) + 8) / 9) }}
	for i := page * 9; i < len(titles) && i < (page+1)*9; i++ {
		steps = append(steps, func() (int, error) { return m.a.number(i - page*9 + 1) }, func() (int, error) { return m.a.play(titles[i]) })
	}
	last := "next"
	if (page+1)*9 >= len(titles) {
		last = "last"
	}
	steps = append(steps, func() (int, error) { return m.prompt(last) })
	for _, step := range steps {
		key, err := step()
		if err != nil || key != 0 {
			return key, err
		}
	}
	return 0, nil
}

func (m *menu) pickPage(pages int) (int, error) {
	for failures := 0; failures < 3; {
		key, err := m.prompt("pick-page")
		if err != nil {
			return -1, err
		}
		digits := ""
		for {
			if key == 0 {
				key, err = m.a.wait()
				if err != nil {
					return -1, err
				}
				if key == 0 {
					break
				}
			}
			if key == '0' && digits == "" {
				return -1, nil
			}
			if key == '*' {
				break
			}
			if key == '#' {
				n, _ := strconv.Atoi(digits)
				if n >= 1 && n <= pages {
					return n - 1, nil
				}
				break
			}
			if key < '0' || key > '9' || len(digits) >= 3 {
				break
			}
			digits += string(rune(key))
			key = 0
		}
		if key != '*' && key != 0 {
			if _, err := m.prompt("invalid"); err != nil {
				return -1, err
			}
		}
		if key != '*' {
			failures++
		}
	}
	return -1, nil
}

func (m *menu) book(b Book) error {
	p := m.loadProgress(b)
	failures := 0
	for failures < 3 {
		key, err := m.title(filepath.Join(bookDir(m.root, b), "title"))
		if err != nil {
			return err
		}
		if key == 0 && p.Started {
			key, err = m.prompt("resume")
			if err != nil {
				return err
			}
		}
		if key == 0 {
			key, err = m.prompt("book")
			if err != nil {
				return err
			}
		}
		if key == 0 {
			key, err = m.a.wait()
			if err != nil {
				return err
			}
		}
		play := false
		switch key {
		case '0':
			return nil
		case '*':
			continue
		case '1':
			play = p.Started
		case '2':
			p.Chapter, p.Seconds = 0, 0
			play = true
		case '3':
			titles := make([]string, len(b.Chapters))
			for i := range titles {
				titles[i] = filepath.Join(bookDir(m.root, b), chapterName(i)+"-title")
			}
			page := 0
			i, e := m.browse("chapters", titles, &page)
			if e != nil {
				return e
			}
			if i < 0 {
				continue
			}
			p.Chapter, p.Seconds = i, 0
			play = true
		}
		if !play {
			failures++
			continue
		}
		failures = 0
		if !ready(m.root, b) {
			_, err = m.prompt("unavailable")
			return err
		}
		if err = m.listen(b, &p); err != nil {
			return err
		}
	}
	return nil
}
