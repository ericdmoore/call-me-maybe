package reminder

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

type scriptedKey struct{ prompt, keys string }
type phone struct {
	t                      *testing.T
	input                  bytes.Buffer
	commands               []string
	keys                   []scriptedKey
	wait                   string
	spool, handset, marker string
	hangupAt               string
	status                 int
	recordEnd              int
	afterRecord            func()
	failQueue              bool
}

func newPhone(t *testing.T, spool string, keys ...scriptedKey) (*AGI, *phone) {
	t.Helper()
	p := &phone{t: t, spool: spool, handset: "kitchen", marker: "kitchen", status: 6, recordEnd: int('#'), keys: keys}
	p.input.WriteString("agi_channel: PJSIP/kitchen-00000001\nagi_callerid: 5550100\n\n")
	a, err := Connect(&p.input, p)
	if err != nil {
		t.Fatal(err)
	}
	return a, p
}

func (p *phone) Write(b []byte) (int, error) {
	cmd := strings.TrimSpace(string(b))
	p.commands = append(p.commands, cmd)
	result, value := 0, ""
	switch {
	case p.hangupAt != "" && strings.Contains(cmd, p.hangupAt):
		result = -1
	case strings.HasPrefix(cmd, "GET FULL VARIABLE"):
		result = 1
		switch {
		case strings.Contains(cmd, "CHANNEL(endpoint)"):
			value = p.handset
		case strings.Contains(cmd, "CMM_REMINDER_HANDSET"):
			value = p.marker
		case strings.Contains(cmd, "ASTSPOOLDIR"):
			value = p.spool
		case strings.Contains(cmd, "CMM_REMINDER_MEDIA"):
			value = "call-me-maybe/system"
		default:
			p.t.Errorf("unexpected variable: %s", cmd)
		}
	case strings.HasPrefix(cmd, "GET OPTION"):
		if len(p.keys) == 0 {
			p.t.Fatalf("unexpected question: %s", cmd)
		}
		k := p.keys[0]
		p.keys = p.keys[1:]
		if !strings.Contains(cmd, "/reminder-"+k.prompt+`"`) {
			p.t.Fatalf("question %s, want %s", cmd, k.prompt)
		}
		if k.keys != "" {
			result = int(k.keys[0])
			p.wait = k.keys[1:]
		}
	case strings.HasPrefix(cmd, "WAIT FOR DIGIT"):
		if p.wait != "" {
			result = int(p.wait[0])
			p.wait = p.wait[1:]
		}
	case strings.HasPrefix(cmd, "CHANNEL STATUS"):
		result = p.status
		if p.failQueue {
			if err := os.Remove(filepath.Join(p.spool, "outgoing")); err != nil {
				p.t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(p.spool, "outgoing"), []byte("blocked"), 0o600); err != nil {
				p.t.Fatal(err)
			}
		}
	case strings.HasPrefix(cmd, "RECORD FILE"):
		parts := strings.Split(cmd, `"`)
		if len(parts) < 3 {
			p.t.Fatal("invalid recording command")
		}
		if err := os.WriteFile(parts[1]+".wav", make([]byte, 100), 0o600); err != nil {
			p.t.Fatal(err)
		}
		result = p.recordEnd
		if p.afterRecord != nil {
			p.afterRecord()
		}
	}
	fmt.Fprintf(&p.input, "200 result=%d", result)
	if value != "" {
		fmt.Fprintf(&p.input, " (%s)", value)
	}
	p.input.WriteByte('\n')
	return len(b), nil
}

func (p *phone) saw(s string) bool { return strings.Contains(strings.Join(p.commands, "\n"), s) }
func (p *phone) finished() {
	p.t.Helper()
	if len(p.keys) != 0 {
		p.t.Fatalf("unasked questions: %v", p.keys)
	}
}

func TestEveryMenuSchedulesAndReadsBackTime(t *testing.T) {
	now := time.Date(2026, 10, 3, 6, 0, 0, 0, chicago(t))
	for _, tc := range []struct {
		code string
		keys []scriptedKey
		want time.Time
	}{
		{"80", []scriptedKey{{"ampm", "2"}, {"time", "0730#"}, {"message", "#"}}, time.Date(2026, 10, 3, 19, 30, 0, 0, now.Location())},
		{"81", []scriptedKey{{"hours", "1"}, {"minutes", "99#"}, {"message", ""}}, now.Add(159 * time.Minute)},
		{"82", []scriptedKey{{"ampm", "1"}, {"time", "730#"}, {"message", "#"}}, time.Date(2026, 10, 4, 7, 30, 0, 0, now.Location())},
	} {
		t.Run(tc.code, func(t *testing.T) {
			s := testStore(t)
			a, p := newPhone(t, s.Spool, tc.keys...)
			if err := Run(a, "menu", tc.code, func() time.Time { return now }); err != nil {
				t.Fatal(err)
			}
			p.finished()
			jobs, err := s.Pending("kitchen")
			if err != nil || len(jobs) != 1 {
				t.Fatalf("pending: %v %v", jobs, err)
			}
			if !jobs[0].Due.Equal(tc.want) || jobs[0].Audio {
				t.Fatalf("saved: %+v", jobs[0])
			}
			if !p.saw(fmt.Sprintf("SAY DATETIME %d", tc.want.Unix())) || !p.saw("reminder-saved") {
				t.Fatal("no time confirmation")
			}
		})
	}
}

func TestInvalidTimesReadAtMostFourDigitsAndNeverSchedule(t *testing.T) {
	now := time.Date(2026, 10, 3, 18, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		entry    string
		readback string
		past     bool
	}{
		{"123456#", `SAY DIGITS "1234"`, false}, {"1260#", `SAY DIGITS "1260"`, false}, {"730#", "", true}, {"73", `SAY DIGITS "73"`, false},
	} {
		s := testStore(t)
		a, p := newPhone(t, s.Spool, scriptedKey{"ampm", "1"}, scriptedKey{"time", tc.entry}, scriptedKey{"try-again", "*"})
		err := Run(a, "menu", "80", func() time.Time { return now })
		if !errors.Is(err, ErrHangup) {
			t.Fatalf("exit: %v", err)
		}
		jobs, _ := s.Pending("kitchen")
		if len(jobs) != 0 {
			t.Fatal("invalid time scheduled")
		}
		if tc.readback != "" && !p.saw(tc.readback) {
			t.Fatal("missing bounded readback")
		}
		if p.saw(`SAY DIGITS "123456`) {
			t.Fatal("overlong input escaped the collector")
		}
		if tc.past && !p.saw("reminder-past") {
			t.Fatal("no *81 suggestion")
		}
	}
}

func TestRestartAndRetryCanSucceed(t *testing.T) {
	s := testStore(t)
	now := time.Now()
	a, p := newPhone(t, s.Spool, scriptedKey{"hours", "*"}, scriptedKey{"hours", "0"}, scriptedKey{"minutes", "0#"}, scriptedKey{"try-again", "1#"}, scriptedKey{"message", "*"}, scriptedKey{"hours", "0"}, scriptedKey{"minutes", "2#"}, scriptedKey{"message", "#"})
	if err := Run(a, "menu", "81", func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	p.finished()
	jobs, _ := s.Pending("kitchen")
	if len(jobs) != 1 || !jobs[0].Due.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("restart: %v", jobs)
	}
}

func TestPendingAcrossAllAppsAnnouncedAndCancelledByThisPhoneOnly(t *testing.T) {
	s := testStore(t)
	now := time.Now()
	for _, code := range []string{"80", "81", "82"} {
		j := testJob(t, "kitchen", now)
		j.Code = code
		mustCommit(t, s, j, now)
	}
	other := testJob(t, "office", now)
	mustCommit(t, s, other, now)
	a, p := newPhone(t, s.Spool, scriptedKey{"cancel", "1"}, scriptedKey{"cancel", "#"}, scriptedKey{"cancel", "1"}, scriptedKey{"new", "#"})
	if err := Run(a, "menu", "80", func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	p.finished()
	for _, code := range []string{"80", "81", "82"} {
		if !p.saw("reminder-kind-" + code) {
			t.Fatalf("did not announce %s", code)
		}
	}
	jobs, _ := s.Pending("kitchen")
	if len(jobs) != 1 {
		t.Fatal("wrong cancellation count")
	}
	jobs, _ = s.Pending("office")
	if len(jobs) != 1 || jobs[0].ID != other.ID {
		t.Fatal("other phone changed")
	}
}

func TestRecordedMessagePlaybackAndCleanup(t *testing.T) {
	s := testStore(t)
	now := time.Now()
	a, p := newPhone(t, s.Spool, scriptedKey{"hours", "0"}, scriptedKey{"minutes", "1#"}, scriptedKey{"message", "1"})
	if err := Run(a, "menu", "81", func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	p.finished()
	jobs, _ := s.Pending("kitchen")
	if len(jobs) != 1 || !jobs[0].Audio {
		t.Fatal("recording not saved")
	}
	j := jobs[0]
	a, p = newPhone(t, s.Spool)
	if err := Run(a, "deliver", j.ID, time.Now); err != nil {
		t.Fatal(err)
	}
	if !p.saw("reminder-call-81") || !p.saw(strings.TrimSuffix(s.audio(j.ID), ".wav")) {
		t.Fatal("no introduction/message")
	}
	if _, err := os.Stat(s.audio(j.ID)); !os.IsNotExist(err) {
		t.Fatal("kept heard recording")
	}
	a, p = newPhone(t, s.Spool)
	if err := Run(a, "deliver", j.ID, time.Now); !errors.Is(err, ErrMissing) {
		t.Fatalf("repeated playback: %v", err)
	}
	if p.saw("STREAM FILE") {
		t.Fatal("played duplicate")
	}
}

func TestHangupsNeverCommitUnfinishedJobs(t *testing.T) {
	for _, point := range []string{"WAIT FOR DIGIT", "RECORD FILE", "CHANNEL STATUS"} {
		t.Run(point, func(t *testing.T) {
			s := testStore(t)
			a, p := newPhone(t, s.Spool, scriptedKey{"hours", "0"}, scriptedKey{"minutes", "1#"}, scriptedKey{"message", "1"})
			p.hangupAt = point
			if err := Run(a, "menu", "81", time.Now); !errors.Is(err, ErrHangup) {
				t.Fatalf("hangup: %v", err)
			}
			jobs, _ := s.Pending("kitchen")
			if len(jobs) != 0 {
				t.Fatal("unfinished job scheduled")
			}
			waves, _ := filepath.Glob(filepath.Join(s.root, "*.wav"))
			if len(waves) != 0 {
				t.Fatal("unfinished recording retained")
			}
		})
	}
}

func TestDelayStartsAfterRecordingAndRecordingRestartDiscardsDraft(t *testing.T) {
	s := testStore(t)
	now := time.Now()
	start := now
	a, p := newPhone(t, s.Spool, scriptedKey{"hours", "0"}, scriptedKey{"minutes", "1#"}, scriptedKey{"message", "1"})
	p.afterRecord = func() { now = now.Add(45 * time.Second) }
	if err := Run(a, "menu", "81", func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	jobs, _ := s.Pending("kitchen")
	if len(jobs) != 1 || !jobs[0].Due.Equal(start.Add(105*time.Second)) {
		t.Fatalf("delay included recording time: %v", jobs)
	}
	if err := s.Cancel("kitchen", jobs[0].ID); err != nil {
		t.Fatal(err)
	}
	a, p = newPhone(t, s.Spool, scriptedKey{"hours", "0"}, scriptedKey{"minutes", "1#"}, scriptedKey{"message", "1"}, scriptedKey{"hours", "0"}, scriptedKey{"minutes", "2#"}, scriptedKey{"message", "#"})
	p.recordEnd = int('*')
	if err := Run(a, "menu", "81", func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	p.finished()
	jobs, _ = s.Pending("kitchen")
	if len(jobs) != 1 || jobs[0].Audio {
		t.Fatal("draft recording survived restart")
	}
	waves, _ := filepath.Glob(filepath.Join(s.root, "*.wav"))
	if len(waves) != 0 {
		t.Fatal("abandoned voice recording retained")
	}
	// The replacement uses the standard message on answer.
	a, p = newPhone(t, s.Spool)
	if err := Run(a, "deliver", jobs[0].ID, time.Now); err != nil {
		t.Fatal(err)
	}
	if !p.saw("reminder-call-81") {
		t.Fatal("standard message missing")
	}
}

func TestStorageFailureIsSpokenWithoutSuccessAcknowledgement(t *testing.T) {
	s := testStore(t)
	a, p := newPhone(t, s.Spool, scriptedKey{"hours", "0"}, scriptedKey{"minutes", "1#"}, scriptedKey{"message", "#"})
	p.failQueue = true
	if Run(a, "menu", "81", time.Now) == nil {
		t.Fatal("failed publication succeeded")
	}
	if !p.saw("reminder-unavailable") || p.saw("reminder-saved") {
		t.Fatal("misleading scheduling acknowledgement")
	}
	meta, _ := filepath.Glob(filepath.Join(s.root, "*.json"))
	if len(meta) != 0 {
		t.Fatal("false pending job remains")
	}
	// Failure opening the spool is also audible, before any input is taken.
	a, p = newPhone(t, "relative")
	if Run(a, "menu", "80", time.Now) == nil || !p.saw("reminder-unavailable") {
		t.Fatal("unavailable spool was silent")
	}
}

func TestFullMenuStillAllowsCancellationAndExplainsLimit(t *testing.T) {
	s := testStore(t)
	now := time.Now()
	var keys []scriptedKey
	for range MaxPending {
		j := testJob(t, "kitchen", now)
		mustCommit(t, s, j, now)
		keys = append(keys, scriptedKey{"cancel", "#"})
	}
	keys = append(keys, scriptedKey{"new", "1"})
	a, p := newPhone(t, s.Spool, keys...)
	if err := Run(a, "menu", "82", time.Now); err != nil {
		t.Fatal(err)
	}
	if !p.saw("reminder-full") {
		t.Fatal("quota not explained")
	}
	p.finished()
}

func TestCallerIDCannotChooseDestinationAndCancelledAnswerPlaysNothing(t *testing.T) {
	s := testStore(t)
	a, p := newPhone(t, s.Spool)
	p.handset = "provider-trunk"
	p.marker = ""
	if Run(a, "menu", "80", time.Now) == nil {
		t.Fatal("untrusted endpoint accepted")
	}
	if p.saw("STREAM FILE") || p.saw("GET OPTION") {
		t.Fatal("untrusted caller reached menu")
	}
	now := time.Now()
	j := testJob(t, "kitchen", now)
	mustCommit(t, s, j, now)
	if err := s.Cancel("kitchen", j.ID); err != nil {
		t.Fatal(err)
	}
	a, p = newPhone(t, s.Spool)
	if err := Run(a, "deliver", j.ID, time.Now); !errors.Is(err, ErrMissing) {
		t.Fatal(err)
	}
	if p.saw("STREAM FILE") {
		t.Fatal("cancelled recording played")
	}
	a, p = newPhone(t, s.Spool)
	p.handset = "office"
	p.marker = "office"
	if Run(a, "deliver", j.ID, time.Now) == nil || p.saw("STREAM FILE") {
		t.Fatal("cross-phone playback")
	}
}

func TestAGIProtocolErrorsNeverEchoInput(t *testing.T) {
	for _, reply := range []string{"510 bad command 5550123", "200 result=5550123oops", "HANGUP", "200 result=-1", ""} {
		var out bytes.Buffer
		a, err := Connect(strings.NewReader("\n"+reply+"\n"), &out)
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = a.command("CHANNEL STATUS")
		if err == nil || strings.Contains(err.Error(), "5550123") {
			t.Fatalf("unsafe error: %v", err)
		}
	}
	if _, err := Connect(strings.NewReader("no blank line"), &bytes.Buffer{}); !errors.Is(err, ErrHangup) {
		t.Fatal(err)
	}
}

func TestMenuPhrasesExistInFreeSystemPrompts(t *testing.T) {
	b, err := os.ReadFile("../../prompts/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		System map[string]string `json:"system"`
	}
	if err := json.Unmarshal(b, &manifest); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("agi.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, match := range regexp.MustCompile(`(?:m\.play|m\.option)\("([a-z-]+)"[,) ]`).FindAllStringSubmatch(string(source), -1) {
		if strings.HasSuffix(match[1], "-") {
			continue
		}
		if manifest.System["reminder-"+match[1]] == "" {
			t.Errorf("missing system phrase %s", match[1])
		}
	}
	for _, name := range []string{"time", "minutes", "standard", "recorded", "menu-80", "menu-81", "menu-82", "kind-80", "kind-81", "kind-82", "call-80", "call-81", "call-82"} {
		if manifest.System["reminder-"+name] == "" {
			t.Errorf("missing %s", name)
		}
	}
}
