package main

import (
	"bytes"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"callmemaybe/internal/host"
)

// vmBox is a box on disk as Asterisk leaves it after one message: the
// spool, voicemail.conf, cmm-mail.env and a hook that records each mail.
type vmBox struct {
	t                       *testing.T
	spool, conf, env, state string
	captures                string
	stt                     *httptest.Server
	sttCalls                int
	sttDown                 bool
	log                     *bytes.Buffer
}

func newVMBox(t *testing.T) *vmBox {
	t.Helper()
	root := t.TempDir()
	b := &vmBox{t: t, spool: filepath.Join(root, "spool"), conf: filepath.Join(root, "voicemail.conf"),
		env: filepath.Join(root, "cmm-mail.env"), state: filepath.Join(root, "state"), captures: filepath.Join(root, "mail"), log: &bytes.Buffer{}}
	os.MkdirAll(b.captures, 0o755)
	os.WriteFile(b.conf, []byte("[general]\nformat = wav49|wav\n[household]\nkids => 4242,Kids,kids@example.com\nfamily => 4242,Family\n"), 0o600)
	hook := filepath.Join(root, "hook")
	script := "#!/bin/sh\nf=\"" + b.captures + "/$$\"\nprintf 'to=%s\\nsubject=%s\\n' \"$MAIL_TO\" \"$1\" > \"$f\"\ncat >> \"$f\"\n"
	os.WriteFile(hook, []byte(script), 0o755)
	b.stt = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.sttCalls++
		if b.sttDown {
			http.Error(w, "model loading", http.StatusServiceUnavailable)
			return
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "no file", 400)
			return
		}
		audio, _ := io.ReadAll(f)
		if !bytes.HasPrefix(audio, []byte("RIFF")) {
			http.Error(w, "not a wav", 400)
			return
		}
		io.WriteString(w, `{"text":"Hi, it's Maddie. Call me back."}`)
	}))
	t.Cleanup(b.stt.Close)
	b.settings("MAIL_HOOK=" + hook)
	prev := voicemailLog
	voicemailLog = b.log
	t.Cleanup(func() { voicemailLog = prev })
	return b
}

func (b *vmBox) settings(lines ...string) {
	os.WriteFile(b.env, []byte(strings.Join(lines, "\n")+"\n"), 0o640)
}

func (b *vmBox) hookLine() string {
	data, _ := os.ReadFile(b.env)
	return strings.SplitN(string(data), "\n", 2)[0]
}

var vmSeq int

func (b *vmBox) leave(box string) {
	b.t.Helper()
	dir := filepath.Join(b.spool, "household", box, "INBOX")
	os.MkdirAll(dir, 0o750)
	vmSeq++
	base := filepath.Join(dir, fmt.Sprintf("msg%04d", vmSeq))
	txt := "[message]\ncallerid=\"Maddie\" <+15125550142>\norigdate=Thu Oct 23 09:15:00 AM UTC 2026\norigtime=" +
		strconv.FormatInt(time.Now().Unix(), 10) + "\nduration=12\nmsg_id=" + strconv.Itoa(vmSeq) + "\n"
	os.WriteFile(base+".txt", []byte(txt), 0o640)
	os.WriteFile(base+".wav", []byte("RIFF-pcm-"+box), 0o640)
}

// notify runs the command as Asterisk would and returns what the hook saw.
func (b *vmBox) notify(box string) []map[string]string {
	b.t.Helper()
	os.RemoveAll(b.captures)
	os.MkdirAll(b.captures, 0o755)
	rc := runVoicemail([]string{"notify", "-env", b.env, "-spool", b.spool, "-conf", b.conf, "-state", b.state, "household", box, "1", "0", "0"})
	if rc != 0 {
		b.t.Fatalf("notify = %d\n%s", rc, b.log)
	}
	entries, _ := os.ReadDir(b.captures)
	var out []map[string]string
	for _, e := range entries {
		data, _ := os.ReadFile(filepath.Join(b.captures, e.Name()))
		head := strings.SplitN(string(data), "\n", 3)
		m := map[string]string{"to": strings.TrimPrefix(head[0], "to="), "subject": strings.TrimPrefix(head[1], "subject=")}
		if len(head) > 2 {
			m["body"] = head[2]
		}
		out = append(out, m)
	}
	return out
}

func (b *vmBox) links() []os.DirEntry {
	entries, _ := os.ReadDir(filepath.Join(b.state, "links"))
	return entries
}

func TestVoicemailNotifyWithTranscriptAndLinks(t *testing.T) {
	b := newVMBox(t)
	b.settings(b.hookLine(), "MAIL_TO=house@example.com", "STT_ENDPOINT="+b.stt.URL+"/v1/audio/transcriptions", "STT_TIMEOUT_MS=5000",
		`VOICEMAIL_LINK_BASE="http://jepsen.tail0000.ts.net:8095 http://jepsen.local:8095 nonsense"`, "VOICEMAIL_LINK_DAYS=10")
	b.leave("kids")
	mail := b.notify("kids")
	if len(mail) != 1 {
		t.Fatalf("mails = %d\n%s", len(mail), b.log)
	}
	m := mail[0]
	if m["to"] != "kids@example.com" {
		t.Errorf("the box's own address should win over MAIL_TO: to = %q", m["to"])
	}
	if m["subject"] != `Voicemail for kids from "Maddie" <+15125550142>` {
		t.Errorf("subject = %q", m["subject"])
	}
	links := b.links()
	if len(links) != 1 {
		t.Fatalf("links = %d", len(links))
	}
	tok := strings.TrimSuffix(links[0].Name(), ".wav")
	for _, want := range []string{
		"> Hi, it's Maddie. Call me back.",
		"for 10 days",
		"(http://jepsen.tail0000.ts.net:8095/v/" + tok + ")",
		"(http://jepsen.local:8095/v/" + tok + ")",
	} {
		if !strings.Contains(m["body"], want) {
			t.Errorf("body lacks %q:\n%s", want, m["body"])
		}
	}
	if strings.Contains(m["body"], b.spool) {
		t.Errorf("a linked mail must not carry the spool path, which the hook would attach:\n%s", m["body"])
	}
	logs := b.log.String()
	if !strings.Contains(logs, `"nonsense" is not an http(s) root`) || !strings.Contains(logs, "sent kids's new message to kids@example.com") {
		t.Errorf("logs:\n%s", logs)
	}
	if strings.Contains(logs, "Maddie") || strings.Contains(logs, "5550142") || strings.Contains(logs, tok) || strings.Contains(logs, "4242") {
		t.Errorf("a transcript, caller ID, token or PIN reached the log:\n%s", logs)
	}
	data, err := os.ReadFile(filepath.Join(b.state, "links", links[0].Name()))
	if err != nil || string(data) != "RIFF-pcm-kids" {
		t.Errorf("the link holds %q, %v", data, err)
	}
}

func TestVoicemailNotifyWhenTheSpeechServiceIsDown(t *testing.T) {
	b := newVMBox(t)
	b.sttDown = true
	b.settings(b.hookLine(), "MAIL_TO=house@example.com", "STT_ENDPOINT="+b.stt.URL)
	b.leave("family")
	mail := b.notify("family")
	if len(mail) != 1 || b.sttCalls != 1 {
		t.Fatalf("mails %d, stt calls %d\n%s", len(mail), b.sttCalls, b.log)
	}
	if mail[0]["to"] != "house@example.com" {
		t.Errorf("a box with no address of its own goes to MAIL_TO: %q", mail[0]["to"])
	}
	if !strings.Contains(mail[0]["body"], "(no transcript)") || !strings.Contains(b.log.String(), "no transcript for family's message") {
		t.Errorf("body:\n%s\nlogs:\n%s", mail[0]["body"], b.log)
	}
}

// Nothing new configured: the mail the shell script sent, with one line
// saying there is no transcript, and nothing copied anywhere.
func TestVoicemailNotifyWithoutTranscriptionOrLinksIsTheOldMail(t *testing.T) {
	b := newVMBox(t)
	b.settings(b.hookLine(), "MAIL_TO=house@example.com")
	b.leave("family")
	mail := b.notify("family")
	if len(mail) != 1 || b.sttCalls != 0 {
		t.Fatalf("mails %d, stt %d\n%s", len(mail), b.sttCalls, b.log)
	}
	wav := filepath.Join(b.spool, "household", "family", "INBOX", fmt.Sprintf("msg%04d.wav", vmSeq))
	want := "Voicemail for **family** from `\"Maddie\" <+15125550142>`\n\n" +
		"- when: Thu Oct 23 09:15:00 AM UTC 2026\n- length: 12 s\n- box: family@household\n\n" +
		"(no transcript)\n\n[recording](" + wav + ")\n"
	if mail[0]["body"] != want {
		t.Errorf("body =\n%q\nwant\n%q", mail[0]["body"], want)
	}
	if len(b.links()) != 0 {
		t.Error("a link was made with no VOICEMAIL_LINK_BASE")
	}
	// The same box, notified again (the mailbox checked from a phone): no second mail.
	if again := b.notify("family"); len(again) != 0 {
		t.Errorf("re-sent: %v", again)
	}
}

func TestVoicemailNotifyWithNowhereToSend(t *testing.T) {
	b := newVMBox(t)
	b.settings(b.hookLine())
	b.leave("family")
	if mail := b.notify("family"); len(mail) != 0 {
		t.Errorf("mailed %v", mail)
	}
	if !strings.Contains(b.log.String(), "stays in the box only") {
		t.Errorf("logs: %s", b.log)
	}
	// An unreadable settings file is a log line and exit 0, never a failure
	// Asterisk would have to read.
	b.log.Reset()
	if rc := runVoicemail([]string{"notify", "-env", t.TempDir(), "household", "family"}); rc != 0 || !strings.Contains(b.log.String(), "cannot read") {
		t.Errorf("rc %d, logs %s", rc, b.log)
	}
}

func TestVoicemailArgumentsAreChecked(t *testing.T) {
	for _, args := range [][]string{
		nil, {"bogus"}, {"notify"}, {"notify", "household"}, {"notify", "house hold", "kids"},
		{"notify", "household", "../kids"}, {"notify", "household", "kids", "1", "2", "3", "4"},
		// The mailbox becomes the mail's subject: no line break gets that far.
		{"notify", "household", "kids\nBcc: x@example.com"}, {"notify", "household", "kids\r"}, {"notify", "house\nhold", "kids"},
		{"notify", "-nope", "household", "kids"}, {"serve", "extra"},
	} {
		if rc := runVoicemail(args); rc != 2 {
			t.Errorf("%v = %d, want 2", args, rc)
		}
	}
}

func TestVoicemailServeIsQuietWithoutALinkBase(t *testing.T) {
	env := filepath.Join(t.TempDir(), "cmm-mail.env")
	os.WriteFile(env, []byte("MAIL_TO=house@example.com\n"), 0o640)
	if rc := runVoicemail([]string{"serve", "-env", env, "-state", t.TempDir()}); rc != 0 {
		t.Errorf("rc = %d", rc)
	}
	if rc := runVoicemail([]string{"serve", "-env", t.TempDir()}); rc != 1 {
		t.Errorf("an unreadable settings file: rc = %d", rc)
	}
}

func TestLinkDays(t *testing.T) {
	var said []string
	logf := func(f string, a ...any) { said = append(said, fmt.Sprintf(f, a...)) }
	for v, want := range map[string]int{"": 14, "7": 7, "0": 14, "x": 14, "99999": 14} {
		env := secretsFrom(map[string]string{"VOICEMAIL_LINK_DAYS": v})
		if got := linkDays(env, logf); got != want {
			t.Errorf("VOICEMAIL_LINK_DAYS=%q → %d, want %d", v, got, want)
		}
	}
	if len(said) != 3 {
		t.Errorf("warnings = %q", said)
	}
}

func TestVoicemailUnitIsWantedOnlyWithALinkBase(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, "cmm-mail.env")
	t.Setenv("CMM_MAIL_ENV", env)
	w := decideWants(secretsFrom(nil), "", "", "")
	if w.Voicemail || !strings.Contains(w.Why["doorman-voicemail.service"], "not set") {
		t.Errorf("no file: %v %q", w.Voicemail, w.Why["doorman-voicemail.service"])
	}
	os.WriteFile(env, []byte("MAIL_TO=a@example.com\nVOICEMAIL_LINK_BASE=http://jepsen.local:8095\n"), 0o640)
	if w := decideWants(secretsFrom(nil), "", "", ""); !w.Voicemail {
		t.Errorf("link base set but unit not wanted: %q", w.Why["doorman-voicemail.service"])
	}
	if ok, why := voicemailLinksWanted(dir); ok || !strings.Contains(why, "cannot read") {
		t.Errorf("unreadable: %v %q", ok, why)
	}
}

// The wrapper Asterisk runs: arguments checked as before, then doorman
// started detached and the wrapper gone before it finishes.
func TestTheWrapperValidatesAndDetaches(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	record := filepath.Join(dir, "args")
	fake := filepath.Join(dir, "doorman")
	os.WriteFile(fake, []byte("#!/bin/sh\nsleep 1\necho \"$@\" > \""+record+".tmp\" && mv \""+record+".tmp\" \""+record+"\"\n"), 0o755)
	run := func(args ...string) (int, time.Duration) {
		cmd := exec.Command("sh", append([]string{"../../scripts/voicemail-notify"}, args...)...)
		cmd.Env = append(os.Environ(), "DOORMAN="+fake)
		start := time.Now()
		err := cmd.Run()
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), time.Since(start)
		}
		return 0, time.Since(start)
	}
	for _, bad := range [][]string{{}, {"household"}, {"house hold", "kids"}, {"household", "k;ds"}, {"household", "kids", "x"}, {"household", "kids", "1", "2", "3", "4"}} {
		if rc, _ := run(bad...); rc != 2 {
			t.Errorf("%q = %d, want 2", bad, rc)
		}
	}
	rc, took := run("household", "kids", "1", "0", "0")
	if rc != 0 || took > 900*time.Millisecond {
		t.Fatalf("rc %d after %v: Asterisk was kept waiting", rc, took)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if got, err := os.ReadFile(record); err == nil {
			if strings.TrimSpace(string(got)) != "voicemail notify household kids 1 0 0" {
				t.Errorf("doorman got %q", got)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("doorman was never started")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// ── the fence: a listener that hands out recordings ──────────────────────

// `doorman voicemail` is reached from its own file and nothing else, and the
// package imports nothing that holds a credential: the process that serves
// recordings never has the SIP, ARI or account secrets in it.
func TestOnlyTheVoicemailCommandNamesTheVoicemailPackage(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || name == "voicemail.go" || name == "voicemail_test.go" {
			continue
		}
		src, _ := os.ReadFile(name)
		if strings.Contains(string(src), "internal/voicemail") {
			t.Errorf("%s names internal/voicemail — it is reached from `doorman voicemail` and nothing else", name)
		}
	}
	src, err := os.ReadFile("voicemail.go")
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{"secretLookup(", "loadDotEnv(", "config.Load"}
	// Spelled in pieces: the other fences grep every file in this directory
	// for these import paths, this one included.
	for _, pkg := range []string{"ari", "account", "provision", "provider", "syncmachine", "inbox"} {
		forbidden = append(forbidden, "internal/"+pkg)
	}
	for _, forbidden := range forbidden {
		if strings.Contains(string(src), forbidden) {
			t.Errorf("voicemail.go mentions %s: the voicemail process reads cmm-mail.env and nothing of doorman's", forbidden)
		}
	}
}

func TestTheVoicemailPackageImportsNoCredentials(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, filepath.Join("..", "..", "internal", "voicemail"), nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range pkgs {
		for path, file := range pkg.Files {
			for _, imp := range file.Imports {
				p := strings.Trim(imp.Path.Value, `"`)
				if strings.HasPrefix(p, "callmemaybe/") && p != "callmemaybe/internal/stt" {
					t.Errorf("%s imports %s: internal/voicemail may reach the speech client and nothing else of ours", path, p)
				}
			}
		}
	}
	// And nothing under internal/ reaches it.
	root := filepath.Join("..", "..", "internal")
	filepath.WalkDir(root, func(dir string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() || filepath.Base(dir) == "voicemail" {
			return nil
		}
		pkgs, err := parser.ParseDir(fset, dir, nil, parser.ImportsOnly)
		if err != nil {
			return nil
		}
		for _, pkg := range pkgs {
			for path, file := range pkg.Files {
				for _, imp := range file.Imports {
					if strings.Contains(imp.Path.Value, "internal/voicemail") {
						t.Errorf("%s imports internal/voicemail", path)
					}
				}
			}
		}
		return nil
	})
}

func TestTheVoicemailStateDirectoryAgreesWithInitServices(t *testing.T) {
	if got := host.DefaultLayout().VoicemailState; got != voicemailStateDir {
		t.Errorf("init services makes %s but notify and serve use %s", got, voicemailStateDir)
	}
}
