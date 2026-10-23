package voicemail

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"

	"callmemaybe/internal/stt"
)

// Notifier is one externnotify run: Asterisk has saved a message, and the
// house mailbox (or the box's own address) hears about it. Mail is a feed,
// never the record — every failure here leaves the message in the box and
// the lamp lit, and is a log line rather than an exit status Asterisk would
// have to interpret.
type Notifier struct {
	Spool  string // /var/spool/asterisk/voicemail
	Conf   string // /etc/asterisk/voicemail.conf, for each box's own address
	MailTo string // where a box with no address of its own is mailed
	// Send hands one mail to the hook with MAIL_TO set to to.
	Send func(to, subject, body string) error
	// STT transcribes the recording; nil means the mail says "(no transcript)".
	STT stt.Transcriber
	// LinkBases are the http(s) roots the links are printed under, one link
	// per base. None: no link is made and nothing is copied, and the mail
	// points at the file in the box as it always has.
	LinkBases []string
	LinkDays  int
	// StateDir holds links/ and the list of messages already mailed. Empty
	// keeps the old behaviour exactly: the newest message, every run.
	StateDir string
	// Window is how old a message may be and still count as new. Asterisk
	// runs externnotify whenever a box's counts change — a message left,
	// and a message listened to — so without the list and the window a
	// mailbox checked from a phone would mail its newest message again, and
	// a first run after an upgrade would mail the whole INBOX.
	Window time.Duration
	Now    func() time.Time
	Rand   io.Reader
	Logf   func(format string, a ...any)
}

const (
	defaultWindow   = time.Hour
	defaultLinkDays = 14
	keepSent        = 500
	maxAudio        = 32 << 20 // 180 s of 8 kHz PCM is under 3 MiB; this is a fence, not a budget
	maxTranscript   = 4000     // characters; a three-minute message is well under
)

// LinksDir is where the recording links live under a state directory.
func LinksDir(stateDir string) string { return filepath.Join(stateDir, "links") }

func (n *Notifier) logf(format string, a ...any) {
	if n.Logf != nil {
		n.Logf(format, a...)
	}
}

func (n *Notifier) now() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now()
}

func (n *Notifier) linkDays() int {
	if n.LinkDays > 0 {
		return n.LinkDays
	}
	return defaultLinkDays
}

// Run mails each new message in context/mailbox's INBOX. It returns an
// error only for what the operator must fix by hand; everything a message
// can survive is logged and passed over.
func (n *Notifier) Run(ctx context.Context, vmContext, mailbox string) error {
	to := n.recipients(vmContext, mailbox)
	if len(to) == 0 {
		n.logf("no address for %s: voicemail.conf gives the box no email and MAIL_TO is not set; voicemail for %s stays in the box only", mailbox, mailbox)
		return nil
	}

	var sent *sentList
	if n.StateDir != "" {
		s, unlock, err := openSent(n.StateDir)
		if err != nil {
			n.logf("cannot keep the list of mailed messages in %s (%v); mailing the newest message only", n.StateDir, err)
		} else {
			defer unlock()
			sent = s
		}
	}

	msgs, err := Inbox(n.Spool, vmContext, mailbox)
	inbox := filepath.Join(n.Spool, vmContext, mailbox, "INBOX")
	if err != nil || len(msgs) == 0 {
		n.logf("no message found in %s", inbox)
		return nil
	}
	var todo []Message
	if sent == nil {
		todo = msgs[len(msgs)-1:]
	} else {
		window := n.Window
		if window <= 0 {
			window = defaultWindow
		}
		for _, m := range msgs {
			if sent.has(m.Key()) || n.now().Sub(m.When()) > window {
				continue
			}
			todo = append(todo, m)
		}
	}
	if len(todo) == 0 {
		n.logf("nothing new in %s to mail", inbox)
	}
	for _, m := range todo {
		transcript := n.transcribe(ctx, m, mailbox)
		recording := n.recording(m, vmContext, mailbox)
		subject, body := Compose(m, vmContext, mailbox, transcript, recording)
		for _, addr := range to {
			if err := n.Send(addr, subject, body); err != nil {
				n.logf("mail hook failed for %s (%v); the message is still in %s", addr, err, mailbox)
				continue
			}
			n.logf("sent %s's new message to %s", mailbox, addr)
		}
		// Recorded whether or not the hook managed it: a retry on the next
		// count change would arrive at a time nobody could predict.
		if sent != nil {
			sent.add(m.Key())
		}
	}
	if sent != nil {
		if err := sent.save(); err != nil {
			n.logf("cannot save the list of mailed messages (%v)", err)
		}
		if removed, err := Prune(LinksDir(n.StateDir), time.Duration(n.linkDays())*24*time.Hour, n.now()); err == nil && removed > 0 {
			n.logf("pruned %d expired recording link(s)", removed)
		}
	}
	return nil
}

// recipients is the box's own address(es) from voicemail.conf, else MAIL_TO.
func (n *Notifier) recipients(vmContext, mailbox string) []string {
	if n.Conf != "" {
		box, ok, err := LookupBox(n.Conf, vmContext, mailbox)
		switch {
		case err != nil:
			n.logf("cannot read %s (%v); falling back to MAIL_TO", n.Conf, err)
		case ok && len(box.Emails) > 0:
			return box.Emails
		}
	}
	if to := strings.TrimSpace(n.MailTo); to != "" {
		return []string{to}
	}
	return nil
}

func (n *Notifier) transcribe(ctx context.Context, m Message, mailbox string) string {
	if n.STT == nil {
		return ""
	}
	pcm, ok := m.PCM()
	if !ok {
		n.logf("no transcript for %s's message: no PCM .wav beside it", mailbox)
		return ""
	}
	audio, err := readCapped(pcm, maxAudio)
	if err != nil {
		n.logf("no transcript for %s's message: %v", mailbox, err)
		return ""
	}
	text, err := n.STT.Transcribe(ctx, "voicemail.wav", audio)
	if err != nil {
		n.logf("no transcript for %s's message: %v", mailbox, err)
		return ""
	}
	n.logf("transcribed %s's message (%d characters)", mailbox, len(text))
	return text
}

func readCapped(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("recording is over %d MiB", limit>>20)
	}
	return b, nil
}

// recording is the Markdown that says where to hear the message.
func (n *Notifier) recording(m Message, vmContext, mailbox string) string {
	if len(n.LinkBases) == 0 {
		// Exactly what the shell script wrote: a link to the file, which the
		// shipped hook attaches.
		if p, ok := m.anyAudio(); ok {
			return fmt.Sprintf("[recording](%s)", p)
		}
		return fmt.Sprintf("(no recording file found beside %s)", m.Txt)
	}
	inBox := fmt.Sprintf("(the recording is in the box, %s@%s", mailbox, vmContext)
	pcm, ok := m.PCM()
	if !ok {
		if _, found := m.anyAudio(); !found {
			return fmt.Sprintf("(no recording file found beside %s)", m.Txt)
		}
		return inBox + "; there is no PCM .wav beside it to link)"
	}
	if n.StateDir == "" {
		return inBox + ")"
	}
	token, err := MakeLink(LinksDir(n.StateDir), pcm, n.Rand)
	if err != nil {
		n.logf("cannot make a recording link for %s's message (%v)", mailbox, err)
		return inBox + "; the link could not be made)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Listen — on the home network or the tailnet only, for %d days:\n", n.linkDays())
	for _, base := range n.LinkBases {
		link := base + "/v/" + token
		fmt.Fprintf(&b, "\n- [%s](%s)", hostLabel(base), strings.ReplaceAll(link, ")", "%29"))
	}
	return b.String()
}

func hostLabel(base string) string {
	if u, err := url.Parse(base); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return base
}

// ParseLinkBases reads VOICEMAIL_LINK_BASE: space-separated http(s) roots,
// e.g. the box's MagicDNS name and its .local name. Each is normalised to
// scheme://host[:port][/path] without a trailing slash; the ones that are
// not usable are returned separately so the caller can say so.
func ParseLinkBases(s string) (good, bad []string) {
	for _, f := range strings.Fields(s) {
		u, err := url.Parse(f)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			bad = append(bad, f)
			continue
		}
		good = append(good, strings.TrimRight(u.Scheme+"://"+u.Host+u.EscapedPath(), "/"))
	}
	return good, bad
}

// Compose is the mail: the shell script's subject and body, with the
// transcript between the facts and the recording.
func Compose(m Message, vmContext, mailbox, transcript, recording string) (subject, body string) {
	caller := clean(m.CallerID)
	if caller == "" {
		caller = "unknown"
	}
	subject = "Voicemail for " + mailbox + " from " + caller
	var b strings.Builder
	// The caller ID is a code span: "<+1512…>" would otherwise be read as an
	// HTML tag by the Markdown renderer and vanish from the body.
	fmt.Fprintf(&b, "Voicemail for **%s** from `%s`\n\n", mailbox, strings.ReplaceAll(caller, "`", "'"))
	fmt.Fprintf(&b, "- when: %s\n- length: %s s\n- box: %s@%s\n", orQ(clean(m.OrigDate)), orQ(clean(m.Duration)), mailbox, vmContext)
	if transcript = quoteTranscript(transcript); transcript != "" {
		fmt.Fprintf(&b, "\n> %s\n", transcript)
	} else {
		b.WriteString("\n(no transcript)\n")
	}
	fmt.Fprintf(&b, "\n%s\n", recording)
	return subject, b.String()
}

func orQ(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

// clean drops control characters, so nothing from a metadata file can
// start a new line in a subject or a body.
func clean(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s))
}

// quoteTranscript makes the speech service's text one safe Markdown
// paragraph: whitespace collapsed, length bounded, and the characters that
// would turn words into markup, links or HTML escaped. The text is what
// somebody said to the house; it must read as that and nothing else.
func quoteTranscript(s string) string {
	s = strings.Join(strings.Fields(clean(strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		return r
	}, s))), " ")
	if s == "" {
		return ""
	}
	if r := []rune(s); len(r) > maxTranscript {
		s = string(r[:maxTranscript]) + "…"
	}
	var b strings.Builder
	for i, r := range s {
		switch r {
		case '\\', '`', '*', '_', '[', ']', '<', '>', '|', '~':
			b.WriteByte('\\')
		case '#', '-', '+', '=':
			if i == 0 {
				b.WriteByte('\\')
			}
		case '.', ')':
			// "1. " at the start would open a list.
			if isDigits(s[:i]) && i > 0 {
				b.WriteByte('\\')
			}
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ── the list of messages already mailed ─────────────────────────────────

type sentList struct {
	path string
	keys []string
	set  map[string]bool
}

// openSent takes the lock (two messages left a second apart start two runs;
// the second waits and then finds the first already mailed) and reads the
// list.
func openSent(dir string) (*sentList, func(), error) {
	if err := ensureDir(dir); err != nil {
		return nil, nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "notified.lock"), os.O_RDWR|os.O_CREATE, 0o640)
	if err != nil {
		return nil, nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		_ = lock.Close()
		return nil, nil, err
	}
	unlock := func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); _ = lock.Close() }
	s := &sentList{path: filepath.Join(dir, "notified"), set: map[string]bool{}}
	if f, err := os.Open(s.path); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			s.add(strings.TrimSpace(sc.Text()))
		}
		_ = f.Close()
	}
	return s, unlock, nil
}

func (s *sentList) has(k string) bool { return s.set[k] }

func (s *sentList) add(k string) {
	if k == "" || s.set[k] {
		return
	}
	s.set[k] = true
	s.keys = append(s.keys, k)
}

func (s *sentList) save() error {
	keys := s.keys
	if len(keys) > keepSent {
		keys = keys[len(keys)-keepSent:]
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".notified-*")
	if err != nil {
		return err
	}
	_, err = io.WriteString(tmp, strings.Join(keys, "\n")+"\n")
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o640)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), s.path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
	}
	return err
}
