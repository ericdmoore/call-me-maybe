// Package ownbook is a handset's own additions to its phone book: the
// numbers filed from the phone with *88 (s24). One vCard 3.0 file per
// handset, written atomically, read back by the directory. It is a
// directory and nothing more — never an admission list — which is why it is
// its own package and not a contacts.toml source.
//
// The file is deliberately plain vCard so a person can open it, and
// deliberately a subset: FN, TEL, UID and REV, nothing else. UID is what
// lets a later transcription rename an entry in place; the contacts
// package ignores UID, which is one more reason this reader is separate.
package ownbook

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Entry is one filed number.
type Entry struct {
	UID   string // "<handset>-<unix seconds>", the spool file's stem
	Name  string // the transcribed name, or the number until there is one
	E164  string
	Added time.Time
	Named bool // true once a transcription has replaced the placeholder
}

// Book is one handset's additions, in the order they were filed.
type Book struct {
	Handset string
	Entries []Entry
}

// Path is where a handset's book lives under dir.
func Path(dir, handset string) string { return filepath.Join(dir, handset+".vcf") }

// Load reads a handset's book. A missing file is an empty book, not an
// error: every phone starts with no additions.
func Load(dir, handset string) (*Book, error) {
	b := &Book{Handset: handset}
	data, err := os.ReadFile(Path(dir, handset))
	if errors.Is(err, os.ErrNotExist) {
		return b, nil
	}
	if err != nil {
		return nil, err
	}
	b.Entries, err = parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", Path(dir, handset), err)
	}
	return b, nil
}

// Save writes the book atomically beside its final name, 0640 so the
// directory service (same group) can read it and nobody else can.
func Save(dir string, b *Book) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	final := Path(dir, b.Handset)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, b.Bytes(), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}

// Upsert files a number. A second filing of the same number keeps the
// earlier entry (and its name, if it has one) rather than duplicating it,
// and reports false.
func (b *Book) Upsert(e Entry) bool {
	for _, have := range b.Entries {
		if have.E164 == e.E164 || have.UID == e.UID {
			return false
		}
	}
	b.Entries = append(b.Entries, e)
	return true
}

// Rename gives the entry with uid its transcribed name. It reports false
// when there is no such entry — filed on another phone, or removed.
func (b *Book) Rename(uid, name string) bool {
	for i := range b.Entries {
		if b.Entries[i].UID == uid {
			b.Entries[i].Name = name
			b.Entries[i].Named = true
			return true
		}
	}
	return false
}

// Find returns the entry with uid.
func (b *Book) Find(uid string) (Entry, bool) {
	for _, e := range b.Entries {
		if e.UID == uid {
			return e, true
		}
	}
	return Entry{}, false
}

// Bytes is the file's content. Entries are kept in filing order; a book is
// a log of what the phone did, and the phone sorts its own display.
func (b *Book) Bytes() []byte {
	var out bytes.Buffer
	for _, e := range b.Entries {
		fmt.Fprintf(&out, "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:%s\r\nFN:%s\r\nTEL;TYPE=cell:%s\r\nREV:%s\r\n",
			escape(e.UID), escape(e.Name), e.E164, e.Added.UTC().Format("20060102T150405Z"))
		if e.Named {
			out.WriteString("X-CMM-NAMED:1\r\n")
		}
		out.WriteString("END:VCARD\r\n")
	}
	return out.Bytes()
}

func parse(data []byte) ([]Entry, error) {
	var entries []Entry
	var cur *Entry
	sc := bufio.NewScanner(bytes.NewReader(data))
	line := 0
	for sc.Scan() {
		line++
		l := strings.TrimRight(sc.Text(), "\r")
		key, val, _ := strings.Cut(l, ":")
		key = strings.ToUpper(strings.SplitN(key, ";", 2)[0])
		switch key {
		case "BEGIN":
			cur = &Entry{}
		case "END":
			if cur == nil {
				return nil, fmt.Errorf("line %d: END without BEGIN", line)
			}
			if cur.UID == "" || cur.E164 == "" {
				return nil, fmt.Errorf("line %d: entry without UID or TEL", line)
			}
			entries = append(entries, *cur)
			cur = nil
		case "UID":
			if cur != nil {
				cur.UID = unescape(val)
			}
		case "FN":
			if cur != nil {
				cur.Name = unescape(val)
			}
		case "TEL":
			if cur != nil {
				cur.E164 = strings.TrimSpace(val)
			}
		case "REV":
			if cur != nil {
				cur.Added, _ = time.Parse("20060102T150405Z", strings.TrimSpace(val))
			}
		case "X-CMM-NAMED":
			if cur != nil {
				cur.Named = strings.TrimSpace(val) == "1"
			}
		}
	}
	if cur != nil {
		return nil, errors.New("unterminated entry")
	}
	return entries, sc.Err()
}

// Handsets lists the handsets that have a book under dir, sorted.
func Handsets(dir string) ([]string, error) {
	names, err := filepath.Glob(filepath.Join(dir, "*.vcf"))
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, strings.TrimSuffix(filepath.Base(n), ".vcf"))
	}
	sort.Strings(out)
	return out, nil
}

func escape(s string) string {
	r := strings.NewReplacer("\\", "\\\\", "\n", "\\n", ",", "\\,", ";", "\\;")
	return r.Replace(s)
}

func unescape(s string) string {
	r := strings.NewReplacer("\\\\", "\\", "\\n", "\n", "\\,", ",", "\\;", ";")
	return r.Replace(s)
}
