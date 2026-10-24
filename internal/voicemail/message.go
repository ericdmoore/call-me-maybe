package voicemail

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Message is one saved message, read from the msgNNNN.txt Asterisk writes
// beside the audio.
type Message struct {
	Txt      string // the metadata file's path
	CallerID string // as Asterisk wrote it: "Name" <+15125550123>
	OrigDate string
	OrigTime int64 // seconds since the epoch; 0 when absent
	Duration string
	MsgID    string // msg_id, unique per message where Asterisk writes one
	modTime  time.Time
}

// PCM is the message's PCM .wav when there is one. The box stores
// `wav49|wav`: the .WAV is GSM in a WAV wrapper, which browsers do not play
// and speech services decode unevenly; the lower-case .wav is what both want.
func (m Message) PCM() (string, bool) {
	p := strings.TrimSuffix(m.Txt, ".txt") + ".wav"
	st, err := os.Stat(p)
	return p, err == nil && st.Mode().IsRegular()
}

// anyAudio is the PCM file, else the WAV49 one: what the email linked before
// it could link anything else.
func (m Message) anyAudio() (string, bool) {
	if p, ok := m.PCM(); ok {
		return p, true
	}
	p := strings.TrimSuffix(m.Txt, ".txt") + ".WAV"
	_, err := os.Stat(p)
	return p, err == nil
}

// Key identifies the message across app_voicemail's renumbering: msg_id
// where Asterisk writes one, else when it arrived and how long it is. It
// carries no caller ID, because it is written to disk.
func (m Message) Key() string {
	if m.MsgID != "" {
		return m.MsgID
	}
	return "t" + strconv.FormatInt(m.OrigTime, 10) + "-" + m.Duration
}

// When is the message's arrival: origtime, else the file's own time.
func (m Message) When() time.Time {
	if m.OrigTime > 0 {
		return time.Unix(m.OrigTime, 0)
	}
	return m.modTime
}

// Inbox lists the messages in one mailbox's INBOX, oldest first by file
// name, which is the order app_voicemail numbers them in.
func Inbox(spool, context, mailbox string) ([]Message, error) {
	dir := filepath.Join(spool, context, mailbox, "INBOX")
	names, err := filepath.Glob(filepath.Join(dir, "msg*.txt"))
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	var out []Message
	for _, n := range names {
		m, err := readMessage(n)
		if err != nil {
			continue // removed between the listing and the read: not ours to report
		}
		out = append(out, m)
	}
	return out, nil
}

func readMessage(path string) (Message, error) {
	f, err := os.Open(path)
	if err != nil {
		return Message{}, err
	}
	defer f.Close()
	m := Message{Txt: path}
	if st, err := f.Stat(); err == nil {
		m.modTime = st.ModTime()
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, value, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "callerid":
			m.CallerID = value
		case "origdate":
			m.OrigDate = value
		case "origtime":
			m.OrigTime, _ = strconv.ParseInt(value, 10, 64)
		case "duration":
			m.Duration = value
		case "msg_id":
			m.MsgID = value
		}
	}
	return m, sc.Err()
}
