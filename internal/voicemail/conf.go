// Package voicemail is the house's side of a voicemail after Asterisk has
// saved it: who the message is for, a transcript, a link that plays the
// recording on the home network, and the mail that carries them. It runs as
// the asterisk user — `doorman voicemail notify` from externnotify and
// `doorman voicemail serve` from its own unit — and is reachable from
// cmd/doorman/voicemail.go and nothing else, asserted by test: the listener
// hands out recordings, so it never shares a process with the daemon, the
// provisioning listener, or anything holding a SIP, ARI or account
// credential. app_voicemail still records, stores and lights the lamp;
// nothing here is on a call.
package voicemail

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Box is one mailbox as voicemail.conf declares it, reduced to what a
// notification needs. The PIN is the first field of the line and is dropped
// as the line is split — it is never held, so nothing here can print it.
type Box struct {
	Context, Mailbox string
	FullName         string
	Emails           []string
}

// maxIncludeDepth bounds #include chains; Asterisk itself stops at a
// similar depth, and a loop must end somewhere other than the stack.
const maxIncludeDepth = 8

// LookupBox reads Asterisk's voicemail configuration at path — following
// #include and #tryinclude, which is how the generated
// voicemail_handsets.conf arrives — and returns the mailbox declared in
// context. ok is false when no line declares it. A file that cannot be read
// is an error only for the top-level path: an include that is missing is
// what #tryinclude is for, and a broken one makes Asterisk refuse the whole
// file anyway, which is a louder signal than anything said here.
func LookupBox(path, context, mailbox string) (Box, bool, error) {
	p := &confParser{dir: filepath.Dir(path), want: context, box: mailbox, seen: map[string]bool{}}
	if err := p.file(path, 0, true); err != nil {
		return Box{}, false, err
	}
	return p.found, p.ok, nil
}

type confParser struct {
	dir       string // relative includes resolve here, as Asterisk resolves them against astetcdir
	want, box string
	seen      map[string]bool
	section   string // carried across includes, as Asterisk's parser carries its current category
	inBlock   bool   // inside a ;-- block comment --;
	found     Box
	ok        bool
}

func (p *confParser) file(path string, depth int, top bool) error {
	if depth > maxIncludeDepth {
		return nil
	}
	abs, err := filepath.Abs(path)
	if err == nil {
		if p.seen[abs] {
			return nil
		}
		p.seen[abs] = true
	}
	f, err := os.Open(path)
	if err != nil {
		if top {
			return err
		}
		return nil
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		p.line(sc.Text(), depth)
	}
	return nil
}

var includeLine = regexp.MustCompile(`^#\s*(include|tryinclude)\s+(.+)$`)

func (p *confParser) line(raw string, depth int) {
	line := strings.TrimSpace(p.stripComments(raw))
	if line == "" {
		return
	}
	if m := includeLine.FindStringSubmatch(line); m != nil {
		for _, inc := range p.includes(m[2]) {
			_ = p.file(inc, depth+1, false)
		}
		return
	}
	if strings.HasPrefix(line, "#") {
		return // #exec and anything else: never run, never followed
	}
	if strings.HasPrefix(line, "[") {
		// [household], [household](+), [tmpl](!): the name is the context.
		if end := strings.IndexByte(line, ']'); end > 0 {
			p.section = strings.TrimSpace(line[1:end])
		}
		return
	}
	if p.section != p.want {
		return
	}
	key, value, ok := splitAssignment(line)
	if !ok || key != p.box {
		return
	}
	// pin,Full Name,email[,pager[,options]] — fields[0] is discarded here
	// and goes no further.
	fields := strings.Split(value, ",")
	b := Box{Context: p.want, Mailbox: p.box}
	if len(fields) > 1 {
		b.FullName = strings.TrimSpace(fields[1])
	}
	if len(fields) > 2 {
		b.Emails = emailsOf(fields[2])
	}
	// A box declared twice takes the later line, as app_voicemail's own
	// append_mailbox overwrites the earlier one.
	p.found, p.ok = b, true
}

// stripComments removes ; comments (and \; is a literal semicolon) and
// ;-- block comments --;, which may span lines.
func (p *confParser) stripComments(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if p.inBlock {
			if strings.HasPrefix(s[i:], "--;") {
				p.inBlock = false
				i += 2
			}
			continue
		}
		switch {
		case s[i] == '\\' && i+1 < len(s) && s[i+1] == ';':
			out.WriteByte(';')
			i++
		case strings.HasPrefix(s[i:], ";--"):
			p.inBlock = true
			i += 2
		case s[i] == ';':
			return out.String()
		default:
			out.WriteByte(s[i])
		}
	}
	return out.String()
}

// includes resolves the argument of an #include: quoted or not, relative to
// the configuration directory, globbed as Asterisk globs it.
func (p *confParser) includes(arg string) []string {
	arg = strings.TrimSpace(arg)
	arg = strings.Trim(arg, `"<>`)
	if arg == "" {
		return nil
	}
	if !filepath.IsAbs(arg) {
		arg = filepath.Join(p.dir, arg)
	}
	if strings.ContainsAny(arg, "*?[") {
		matches, _ := filepath.Glob(arg)
		return matches
	}
	return []string{arg}
}

// splitAssignment takes "key => value" or "key = value".
func splitAssignment(line string) (key, value string, ok bool) {
	if i := strings.Index(line, "=>"); i >= 0 {
		if j := strings.IndexByte(line, '='); j < 0 || j >= i {
			return strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+2:]), true
		}
	}
	k, v, ok := strings.Cut(line, "=")
	return strings.TrimSpace(k), strings.TrimSpace(v), ok
}

// address is deliberately narrow: what comes out of here is handed to a mail
// hook as MAIL_TO and so reaches a command line. No spaces, no leading dash,
// no quoting or list characters, exactly one @.
var address = regexp.MustCompile(`^[A-Za-z0-9._%+=!#$&'*/?^{}~][A-Za-z0-9._%+=!#$&'*/?^{}~-]*@[A-Za-z0-9.-]+$`)

// emailsOf splits app_voicemail's email field, which may name several
// addresses separated by |, and keeps the ones shaped like an address.
func emailsOf(field string) []string {
	var out []string
	for _, a := range strings.Split(field, "|") {
		a = strings.TrimSpace(a)
		if ValidAddress(a) {
			out = append(out, a)
		}
	}
	return out
}

// ValidAddress reports whether a is an address this package will pass to a
// mail hook.
func ValidAddress(a string) bool {
	return address.MatchString(a) && !strings.Contains(a, "..")
}
