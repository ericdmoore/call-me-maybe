package voicemail

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeSTT struct {
	text  string
	err   error
	calls int
}

func (f *fakeSTT) Transcribe(_ context.Context, name string, audio []byte) (string, error) {
	f.calls++
	if !strings.HasPrefix(string(audio), "RIFF") || name != "voicemail.wav" {
		return "", fmt.Errorf("sent %q %q", name, audio)
	}
	return f.text, f.err
}

type sentMail struct{ to, subject, body string }

// wav49Distinct is false on a case-insensitive filesystem (a Mac's
// default), where msg0000.WAV and msg0000.wav are one file and a box with
// only the WAV49 recording cannot be built. Asterisk's own is case-sensitive.
func wav49Distinct(t *testing.T) bool {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "probe.WAV"), nil, 0o600)
	_, err := os.Stat(filepath.Join(dir, "probe.wav"))
	return err != nil
}

// spoolMessage writes msgNNNN.txt (and the audio formats asked for) into
// household/<box>/INBOX.
func spoolMessage(t *testing.T, spool, box string, n int, origtime int64, id string, formats ...string) string {
	t.Helper()
	dir := filepath.Join(spool, "household", box, "INBOX")
	os.MkdirAll(dir, 0o750)
	base := filepath.Join(dir, fmt.Sprintf("msg%04d", n))
	txt := fmt.Sprintf(";\n; Message Information file\n;\n[message]\norigmailbox=%s\ncontext=voicemail-drop\ncallerid=\"Maddie\" <+15125550142>\norigdate=Thu Oct 23 09:15:00 AM UTC 2026\norigtime=%d\nduration=12\n", box, origtime)
	if id != "" {
		txt += "msg_id=" + id + "\n"
	}
	os.WriteFile(base+".txt", []byte(txt), 0o640)
	for _, f := range formats {
		os.WriteFile(base+"."+f, []byte("RIFF-"+f), 0o640)
	}
	return base
}

func notifier(t *testing.T, spool string, out *[]sentMail, logs *[]string) *Notifier {
	t.Helper()
	return &Notifier{
		Spool: spool, MailTo: "house@example.com", StateDir: filepath.Join(t.TempDir(), "state"),
		Send: func(to, subject, body string) error {
			*out = append(*out, sentMail{to, subject, body})
			return nil
		},
		Logf: func(f string, a ...any) { *logs = append(*logs, fmt.Sprintf(f, a...)) },
	}
}

func TestComposeKeepsTheShellScriptsShape(t *testing.T) {
	m := Message{Txt: "/s/msg0000.txt", CallerID: "\"Maddie\" <+15125550142>", OrigDate: "Thu Oct 23", Duration: "12"}
	subject, body := Compose(m, "household", "kids", "", "[recording](/s/msg0000.wav)")
	if subject != "Voicemail for kids from \"Maddie\" <+15125550142>" {
		t.Errorf("subject = %q", subject)
	}
	want := "Voicemail for **kids** from `\"Maddie\" <+15125550142>`\n\n" +
		"- when: Thu Oct 23\n- length: 12 s\n- box: kids@household\n" +
		"\n(no transcript)\n" +
		"\n[recording](/s/msg0000.wav)\n"
	if body != want {
		t.Errorf("body =\n%s\nwant\n%s", body, want)
	}
	subject, body = Compose(Message{CallerID: "x`y\r\nBcc: z"}, "c", "b", "  Hello,\nit's   Maddie. ", "r")
	if subject != "Voicemail for b from x`yBcc: z" || !strings.Contains(body, "from `x'yBcc: z`") {
		t.Errorf("control characters or backticks survived: %q / %q", subject, body)
	}
	if !strings.Contains(body, "\n> Hello, it's Maddie.\n") || !strings.Contains(body, "- when: ?\n- length: ? s") {
		t.Errorf("body = %q", body)
	}
}

func TestTranscriptIsTextNotMarkup(t *testing.T) {
	cases := map[string]string{
		"call me back":                          "call me back",
		"<script>alert(1)</script>":             `\<script\>alert(1)\</script\>`,
		"see [this](http://x) *now* _please_":   `see \[this\](http://x) \*now\* \_please\_`,
		"# not a heading":                       `\# not a heading`,
		"- not a list":                          `\- not a list`,
		"1. not a list either":                  `1\. not a list either`,
		"line one\n\n> line two":                `line one \> line two`,
		"back`tick \\ pipe | tilde ~":           "back\\`tick \\\\ pipe \\| tilde \\~",
		"   ":                                   "",
		"it costs 1.50 at 3-4 pm, not 2+2=5 #1": "it costs 1.50 at 3-4 pm, not 2+2=5 #1",
	}
	for in, want := range cases {
		if got := quoteTranscript(in); got != want {
			t.Errorf("quoteTranscript(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("a", maxTranscript+10)
	if got := quoteTranscript(long); len([]rune(got)) != maxTranscript+1 || !strings.HasSuffix(got, "…") {
		t.Errorf("long transcript = %d runes", len([]rune(got)))
	}
}

func TestParseLinkBases(t *testing.T) {
	good, bad := ParseLinkBases("http://jepsen.tail0000.ts.net:8095/ https://jepsen.local:8095/vm  ftp://x http://user:pw@x/ http:// http://x/?q=1 jepsen.local")
	if !reflect.DeepEqual(good, []string{"http://jepsen.tail0000.ts.net:8095", "https://jepsen.local:8095/vm"}) {
		t.Errorf("good = %v", good)
	}
	if len(bad) != 5 {
		t.Errorf("bad = %v", bad)
	}
}

// Asterisk runs externnotify on every count change — a message left, and a
// message listened to. Only the first is news.
func TestARunMailsEachNewMessageOnce(t *testing.T) {
	spool := t.TempDir()
	now := time.Date(2026, 10, 23, 9, 30, 0, 0, time.UTC)
	spoolMessage(t, spool, "kids", 0, now.Add(-48*time.Hour).Unix(), "old-1", "wav") // the backlog before the upgrade
	spoolMessage(t, spool, "kids", 1, now.Add(-2*time.Minute).Unix(), "new-1", "wav", "WAV")
	spoolMessage(t, spool, "kids", 2, now.Add(-1*time.Minute).Unix(), "", "wav") // no msg_id: keyed by time
	var out []sentMail
	var logs []string
	n := notifier(t, spool, &out, &logs)
	n.Now = func() time.Time { return now }
	if err := n.Run(context.Background(), "household", "kids"); err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || !strings.Contains(out[0].body, "msg0001.wav") || !strings.Contains(out[1].body, "msg0002.wav") {
		t.Fatalf("first run sent %d: %+v", len(out), out)
	}
	// The mailbox is checked from a phone: externnotify again, nothing new.
	out = nil
	if err := n.Run(context.Background(), "household", "kids"); err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("a second run re-sent %d", len(out))
	}
	// app_voicemail renumbers after msg0000 is deleted: the same messages,
	// new names, still not news.
	dir := filepath.Join(spool, "household", "kids", "INBOX")
	for _, ext := range []string{"txt", "wav"} {
		os.Remove(filepath.Join(dir, "msg0000."+ext))
		os.Rename(filepath.Join(dir, "msg0001."+ext), filepath.Join(dir, "msg0000."+ext))
		os.Rename(filepath.Join(dir, "msg0002."+ext), filepath.Join(dir, "msg0001."+ext))
	}
	spoolMessage(t, spool, "kids", 2, now.Unix(), "new-2", "wav")
	if err := n.Run(context.Background(), "household", "kids"); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || !strings.Contains(out[0].body, "msg0002.wav") {
		t.Errorf("after renumbering sent %+v", out)
	}
	sent, _ := os.ReadFile(filepath.Join(n.StateDir, "notified"))
	if strings.Contains(string(sent), "555") || strings.Contains(string(sent), "Maddie") {
		t.Errorf("the list of mailed messages carries caller data: %q", sent)
	}
}

// app_voicemail copies a message — forwarded from *97, or left with
// VoiceMail(a&b) — by copying its .txt verbatim: the copy carries the
// original's msg_id and origtime. It is still news to the box it lands in.
func TestACopiedMessageIsMailedToItsNewBox(t *testing.T) {
	spool := t.TempDir()
	now := time.Date(2026, 10, 23, 9, 30, 0, 0, time.UTC)
	var out []sentMail
	var logs []string
	n := notifier(t, spool, &out, &logs)
	n.Now = func() time.Time { return now }
	run := func(box string) {
		t.Helper()
		if err := n.Run(context.Background(), "household", box); err != nil {
			t.Fatal(err)
		}
	}
	// Both boxes are seen once with an old message already in them: the
	// backlog rule still holds for each, and baselines it.
	spoolMessage(t, spool, "kids", 0, now.Add(-48*time.Hour).Unix(), "backlog-kids", "wav")
	spoolMessage(t, spool, "adults", 0, now.Add(-48*time.Hour).Unix(), "backlog-adults", "wav")
	run("kids")
	run("adults")
	if len(out) != 0 {
		t.Fatalf("the backlog was mailed: %+v", out)
	}

	// VoiceMail(kids&adults): one recording, the same id in both boxes.
	spoolMessage(t, spool, "kids", 1, now.Add(-time.Minute).Unix(), "1697040000-00000001", "wav")
	spoolMessage(t, spool, "adults", 1, now.Add(-time.Minute).Unix(), "1697040000-00000001", "wav")
	run("kids")
	run("adults")
	if len(out) != 2 || !strings.Contains(out[0].subject, "kids") || !strings.Contains(out[1].subject, "adults") {
		t.Fatalf("one id in two boxes sent %+v", out)
	}

	// A parent forwards yesterday's message from kids to adults.
	out = nil
	spoolMessage(t, spool, "adults", 2, now.Add(-26*time.Hour).Unix(), "1697000000-00000009", "wav")
	run("adults")
	if len(out) != 1 || !strings.Contains(out[0].body, "msg0002.wav") {
		t.Fatalf("a forwarded day-old message sent %+v; logs %q", out, logs)
	}
	run("adults")
	if len(out) != 1 {
		t.Errorf("the forwarded message was mailed again: %d", len(out))
	}

	// A box never seen before keeps the backlog rule.
	out = nil
	spoolMessage(t, spool, "family", 0, now.Add(-26*time.Hour).Unix(), "1697000000-00000009", "wav")
	run("family")
	if len(out) != 0 {
		t.Errorf("a new box's backlog was mailed: %+v", out)
	}
}

func TestTheSentListKeepsEveryBoxMarker(t *testing.T) {
	dir := t.TempDir()
	s, unlock, err := openSent(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.add(boxKey("household", "kids"))
	for i := 0; i < keepSent+10; i++ {
		s.add(fmt.Sprintf("%skey-%d", boxKey("household", "kids"), i))
	}
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	unlock()
	s, unlock, err = openSent(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if !s.has(boxKey("household", "kids")) || s.has(boxKey("household", "kids")+"key-0") || !s.has(fmt.Sprintf("%skey-%d", boxKey("household", "kids"), keepSent+9)) {
		t.Errorf("trimmed list: marker %v, oldest %v", s.has(boxKey("household", "kids")), s.has(boxKey("household", "kids")+"key-0"))
	}
	if len(s.keys) != keepSent+1 {
		t.Errorf("kept %d keys, want %d", len(s.keys), keepSent+1)
	}
}

func TestWithoutStateTheNewestMessageIsMailedAsBefore(t *testing.T) {
	spool := t.TempDir()
	spoolMessage(t, spool, "kids", 0, 1, "a", "wav")
	spoolMessage(t, spool, "kids", 1, 2, "b", "WAV") // WAV49 only, as an older box format
	var out []sentMail
	var logs []string
	n := notifier(t, spool, &out, &logs)
	n.StateDir = ""
	n.Run(context.Background(), "household", "kids")
	linked := "msg0001.WAV"
	if !wav49Distinct(t) {
		linked = "msg0001.wav"
	}
	if len(out) != 1 || !strings.Contains(out[0].body, "[recording]("+filepath.Join(spool, "household", "kids", "INBOX", linked)+")") {
		t.Errorf("sent %+v", out)
	}
	// Even with link bases, no state directory means nowhere to link from.
	out = nil
	n.LinkBases = []string{"http://jepsen.local:8095"}
	spoolMessage(t, spool, "kids", 2, 3, "c", "wav")
	n.Run(context.Background(), "household", "kids")
	if len(out) != 1 || !strings.Contains(out[0].body, "(the recording is in the box, kids@household)") {
		t.Errorf("sent %+v", out)
	}
}

func TestRecordingSectionWithLinks(t *testing.T) {
	spool := t.TempDir()
	now := time.Now()
	var out []sentMail
	var logs []string
	n := notifier(t, spool, &out, &logs)
	n.LinkBases = []string{"http://jepsen.tail0000.ts.net:8095", "http://jepsen.local:8095"}
	n.LinkDays = 7

	if wav49Distinct(t) {
		spoolMessage(t, spool, "kids", 0, now.Unix(), "w49", "WAV") // no PCM: nothing a browser plays
		n.Run(context.Background(), "household", "kids")
		if len(out) != 1 || !strings.Contains(out[0].body, "no PCM .wav beside it to link") {
			t.Fatalf("sent %+v", out)
		}
		out = nil
	}
	spoolMessage(t, spool, "kids", 1, now.Unix(), "none") // no audio at all
	n.Run(context.Background(), "household", "kids")
	if len(out) != 1 || !strings.Contains(out[0].body, "(no recording file found beside") {
		t.Fatalf("sent %+v", out)
	}
	if entries, _ := os.ReadDir(LinksDir(n.StateDir)); len(entries) != 0 {
		t.Errorf("links made with nothing to link: %d", len(entries))
	}

	// The links directory cannot be made: the mail still goes, saying so.
	out = nil
	os.WriteFile(LinksDir(n.StateDir), []byte("a file in the way"), 0o640)
	spoolMessage(t, spool, "kids", 2, now.Unix(), "blocked", "wav")
	n.Run(context.Background(), "household", "kids")
	if len(out) != 1 || !strings.Contains(out[0].body, "the link could not be made") {
		t.Fatalf("sent %+v", out)
	}
	os.Remove(LinksDir(n.StateDir))

	out = nil
	spoolMessage(t, spool, "kids", 3, now.Unix(), "linked", "wav", "WAV")
	n.Run(context.Background(), "household", "kids")
	if len(out) != 1 {
		t.Fatalf("sent %d", len(out))
	}
	body := out[0].body
	entries, _ := os.ReadDir(LinksDir(n.StateDir))
	if len(entries) != 1 {
		t.Fatalf("links = %d", len(entries))
	}
	tok := strings.TrimSuffix(entries[0].Name(), ".wav")
	for _, want := range []string{
		"for 7 days",
		"- [jepsen.tail0000.ts.net](http://jepsen.tail0000.ts.net:8095/v/" + tok + ")",
		"- [jepsen.local](http://jepsen.local:8095/v/" + tok + ")",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, spool) {
		t.Errorf("with links the mail must not carry the path (the hook would attach it):\n%s", body)
	}
	for _, l := range logs {
		if strings.Contains(l, tok) {
			t.Errorf("a token reached the log: %q", l)
		}
	}
}

func TestRecipientsAndFailures(t *testing.T) {
	spool := t.TempDir()
	conf := filepath.Join(t.TempDir(), "voicemail.conf")
	os.WriteFile(conf, []byte("[household]\nkids => 4242,Kids,kids@example.com|nana@example.com\nfamily => 4242,Family\n"), 0o600)
	now := time.Now()
	spoolMessage(t, spool, "kids", 0, now.Unix(), "k", "wav")
	spoolMessage(t, spool, "family", 0, now.Unix(), "f", "wav")
	var out []sentMail
	var logs []string
	n := notifier(t, spool, &out, &logs)
	n.Conf = conf
	failNana := n.Send
	n.Send = func(to, s, b string) error {
		if to == "nana@example.com" {
			return errors.New("token revoked")
		}
		return failNana(to, s, b)
	}
	n.Run(context.Background(), "household", "kids")
	if len(out) != 1 || out[0].to != "kids@example.com" {
		t.Errorf("kids went to %+v", out)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "mail hook failed for nana@example.com (token revoked); the message is still in kids") {
		t.Errorf("logs = %q", logs)
	}
	out = nil
	n.Run(context.Background(), "household", "family")
	if len(out) != 1 || out[0].to != "house@example.com" {
		t.Errorf("family (no email of its own) went to %+v", out)
	}
	// Neither: a log line, no send, no error.
	out, logs = nil, nil
	n.MailTo = ""
	spoolMessage(t, spool, "family", 1, now.Unix(), "f2", "wav")
	if err := n.Run(context.Background(), "household", "family"); err != nil || len(out) != 0 || !strings.Contains(strings.Join(logs, ""), "stays in the box only") {
		t.Errorf("no address: %v %+v %q", err, out, logs)
	}
	// An unreadable voicemail.conf falls back to MAIL_TO and says why.
	logs = nil
	n.MailTo = "house@example.com"
	n.Conf = filepath.Join(t.TempDir(), "absent.conf")
	if got := n.recipients("household", "kids"); !reflect.DeepEqual(got, []string{"house@example.com"}) || !strings.Contains(strings.Join(logs, ""), "falling back to MAIL_TO") {
		t.Errorf("recipients = %v, logs %q", got, logs)
	}
	// An empty box is a log line.
	logs = nil
	n.Run(context.Background(), "household", "nobody")
	if !strings.Contains(strings.Join(logs, ""), "no message found") {
		t.Errorf("logs = %q", logs)
	}
}

func TestTranscripts(t *testing.T) {
	spool := t.TempDir()
	now := time.Now()
	var out []sentMail
	var logs []string
	n := notifier(t, spool, &out, &logs)
	f := &fakeSTT{text: "Hi, it's Maddie. Call me back."}
	n.STT = f
	spoolMessage(t, spool, "kids", 0, now.Unix(), "a", "wav", "WAV")
	n.Run(context.Background(), "household", "kids")
	if len(out) != 1 || !strings.Contains(out[0].body, "\n> Hi, it's Maddie. Call me back.\n") || strings.Contains(out[0].body, "(no transcript)") {
		t.Fatalf("sent %+v", out)
	}
	for _, l := range logs {
		if strings.Contains(l, "Maddie") {
			t.Errorf("the transcript reached the log: %q", l)
		}
	}
	// The service is down: the mail still goes, saying so.
	out = nil
	f.err = errors.New("stt: alpaca:9001 answered 503")
	spoolMessage(t, spool, "kids", 1, now.Unix(), "b", "wav")
	n.Run(context.Background(), "household", "kids")
	if len(out) != 1 || !strings.Contains(out[0].body, "(no transcript)") {
		t.Fatalf("sent %+v", out)
	}
	// No PCM .wav: nothing is sent to the service.
	if wav49Distinct(t) {
		out = nil
		calls := f.calls
		spoolMessage(t, spool, "kids", 2, now.Unix(), "c", "WAV")
		n.Run(context.Background(), "household", "kids")
		if len(out) != 1 || f.calls != calls || !strings.Contains(out[0].body, "(no transcript)") {
			t.Errorf("WAV49 only: sent %+v, calls %d→%d", out, calls, f.calls)
		}
	}
	if _, err := readCapped(filepath.Join(spool, "absent"), 10); err == nil {
		t.Error("reading nothing succeeded")
	}
	big := filepath.Join(spool, "big.wav")
	os.WriteFile(big, []byte("RIFF-0123456789"), 0o640)
	if _, err := readCapped(big, 4); err == nil {
		t.Error("an oversized recording was read")
	}
}

func TestPrunesExpiredLinksAsItGoes(t *testing.T) {
	spool := t.TempDir()
	var out []sentMail
	var logs []string
	n := notifier(t, spool, &out, &logs)
	links := LinksDir(n.StateDir)
	os.MkdirAll(links, 0o750)
	tok, _ := NewToken(nil)
	old := filepath.Join(links, tok+".wav")
	os.WriteFile(old, []byte("x"), 0o640)
	past := time.Now().Add(-20 * 24 * time.Hour)
	os.Chtimes(old, past, past)
	spoolMessage(t, spool, "kids", 0, time.Now().Unix(), "a", "wav")
	n.Run(context.Background(), "household", "kids")
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Error("an expired link survived a run")
	}
	if !strings.Contains(strings.Join(logs, "\n"), "pruned 1 expired recording link") {
		t.Errorf("logs = %q", logs)
	}
}
