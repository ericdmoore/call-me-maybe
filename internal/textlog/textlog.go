package textlog

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// Package textlog is the inbox's own record of what it did with each text:
// an append-only JSON Lines file in its state directory, beside the seen-id
// set. Its own package, apart from internal/inbox, so that the one other
// reader — `doorman digest` — never links the edge client: the guard that
// keeps texts off every path but `doorman inbox` is a grep for the package
// name, and this file is a log, not a text.
//
// It exists because the journal has one writer — the daemon — and this
// process is not it (s13 M5 is where message.* events reach the journal).
// Until then "which texts acted, which failed" has to live somewhere the
// morning digest can read, and a file owned by the process that decided is
// the honest place. It is an output, never an input: nothing here is read
// back to decide anything (invariant 10, the same reasoning as calls.jsonl).
//
// It holds full sender numbers, because the house mailbox this feeds is the
// audit log and the carrier's own forwarding already carries them. 0600, in
// a 0700 directory, and never the text of an unknown word.

// Record is one line of the log.
type Record struct {
	At     time.Time `json:"at"`
	ID     string    `json:"id"`
	To     string    `json:"to,omitempty"`     // the sender, E.164; who any reply went to
	Person string    `json:"person,omitempty"` // [[people]] id or name
	Word   string    `json:"word,omitempty"`
	Action string    `json:"action,omitempty"`
	Result string    `json:"result"`
	Reply  string    `json:"reply,omitempty"` // what the house said back, if anything
	Detail string    `json:"detail,omitempty"`
}

// Log appends records, rotating once at maxBytes and keeping one
// previous generation — the same bound the call log has, for the same
// reason: a file nobody rotates is a disk nobody meant to fill.
type Log struct {
	path     string
	maxBytes int64
}

const (
	outcomeFile     = "outcomes.jsonl"
	outcomeMaxBytes = 8 << 20
)

// Open prepares the log under dir, creating the directory
// privately if it does not exist.
func Open(dir string) (*Log, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Log{path: filepath.Join(dir, outcomeFile), maxBytes: outcomeMaxBytes}, nil
}

// Append writes one record. A failure is returned, never fatal to the
// caller: the text has already been handled, and a log that could not be
// written must not unhandle it.
func (l *Log) Append(r Record) error {
	if r.At.IsZero() {
		r.At = time.Now()
	}
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if info, err := os.Stat(l.path); err == nil && info.Size()+int64(len(line))+1 > l.maxBytes {
		_ = os.Rename(l.path, l.path+".1")
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// Read returns every record at or after since, oldest first, from
// the previous generation and the current one. A missing log is no
// records, not an error: a house that has never been texted has nothing to
// report. A corrupt line is skipped, because one bad write must not hide a
// day's texts.
func Read(dir string, since time.Time) ([]Record, error) {
	var out []Record
	for _, name := range []string{outcomeFile + ".1", outcomeFile} {
		f, err := os.Open(filepath.Join(dir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for sc.Scan() {
			var r Record
			if json.Unmarshal(sc.Bytes(), &r) != nil {
				continue
			}
			if !r.At.Before(since) {
				out = append(out, r)
			}
		}
		f.Close()
	}
	return out, nil
}
