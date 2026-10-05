package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"callmemaybe/internal/ari"
	"callmemaybe/internal/policy"
)

func f64(v float64) *float64 { return &v }

var ringHandsets = []policy.Handset{
	{ID: "kitchen", Endpoint: "PJSIP/kitchen"},
	{ID: "office", Endpoint: "PJSIP/office"},
	{ID: "conference", Endpoint: "Local/600@internal"},
}

var ringGroups = []policy.Group{
	{ID: "adults", Handsets: []string{"kitchen", "office", "conference"}},
}

func TestRingTargetsExpandGroupsAndSkipPseudoHandsets(t *testing.T) {
	targets, skipped, err := ringTargets([]string{"adults", "kitchen"}, ringHandsets, ringGroups)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || targets[0].Endpoint != "PJSIP/kitchen" || targets[1].Endpoint != "PJSIP/office" {
		t.Fatalf("targets = %+v", targets)
	}
	if len(skipped) != 1 || skipped[0] != "conference" {
		t.Fatalf("skipped = %v, want the conference pseudo-handset", skipped)
	}
	if _, _, err := ringTargets([]string{"attic"}, ringHandsets, ringGroups); err == nil {
		t.Error("an unknown id should be refused, not rung")
	}
	if _, _, err := ringTargets([]string{"conference"}, ringHandsets, ringGroups); err == nil {
		t.Error("nothing ringable should be an error rather than a silent no-op")
	}
}

func TestSpokenBalanceIsTheLowestLowTrunkRoundedDown(t *testing.T) {
	results := []balanceResult{
		{Trunk: "voipms", State: balanceLow, Balance: f64(12.95)},
		{Trunk: "telnyx", State: balanceLow, Balance: f64(3.40)},
		{Trunk: "flowroute", State: balanceOK, Balance: f64(1.00)}, // fine, and not spoken
	}
	trunk, whole, ok := spokenBalance(results)
	if !ok || trunk != "telnyx" || whole != 3 {
		t.Fatalf("spoken = %q %d %v, want telnyx 3", trunk, whole, ok)
	}
	if _, _, ok := spokenBalance([]balanceResult{{Trunk: "x", State: balanceOK, Balance: f64(1)}}); ok {
		t.Error("nothing low means nothing to say")
	}
}

func TestDueToRingRemembersAndForgets(t *testing.T) {
	now := time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC)
	st := ringState{Rang: map[string]ringRecord{
		"voipms": {At: now.Add(-2 * time.Hour), Balance: 12},
		"telnyx": {At: now.Add(-48 * time.Hour), Balance: 3},
		"gone":   {At: now.Add(-1 * time.Hour), Balance: 1},
	}}
	results := []balanceResult{
		{Trunk: "voipms", State: balanceLow, Balance: f64(11)},
		{Trunk: "telnyx", State: balanceLow, Balance: f64(2)},
		{Trunk: "fresh", State: balanceLow, Balance: f64(9)},
		{Trunk: "gone", State: balanceOK, Balance: f64(90)},
	}
	due := dueToRing(&st, results, now, 24*time.Hour)
	if strings.Join(due, ",") != "fresh,telnyx" {
		t.Fatalf("due = %v, want fresh (never rang) and telnyx (rang two days ago)", due)
	}
	if _, still := st.Rang["gone"]; still {
		t.Error("a trunk that recovered should be forgotten so its next dip rings at once")
	}
	if due := dueToRing(&st, results, now, 0); len(due) != 3 {
		t.Errorf("--repeat 0 should ring every run, got %v", due)
	}
}

// fakeAnnouncer scripts one handset per endpoint: whether it answers, and
// a channel that rings, maybe comes up, and then is gone.
type fakeAnnouncer struct {
	mu       sync.Mutex
	answers  map[string]bool
	polls    map[string]int
	args     []string
	hangups  []string
	failNext bool
}

func (f *fakeAnnouncer) Originate(_ context.Context, p ari.OriginateParams) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext {
		return "", errors.New("Application not registered")
	}
	f.args = append(f.args, p.AppArgs)
	return "ch-" + strings.TrimPrefix(p.Endpoint, "PJSIP/"), nil
}

func (f *fakeAnnouncer) Channel(_ context.Context, id string) (ari.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.polls[id]++
	handset := strings.TrimPrefix(id, "ch-")
	switch {
	case f.polls[id] == 1:
		return ari.Channel{ID: id, State: "Ringing"}, nil
	case f.answers[handset] && f.polls[id] == 2:
		return ari.Channel{ID: id, State: "Up"}, nil
	}
	return ari.Channel{}, &ari.Error{Status: http.StatusNotFound, Path: "/channels/" + id}
}

func (f *fakeAnnouncer) Hangup(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hangups = append(f.hangups, id)
	return nil
}

func newFakeAnnouncer(answers map[string]bool) *fakeAnnouncer {
	return &fakeAnnouncer{answers: answers, polls: map[string]int{}}
}

func fastPolls(t *testing.T) {
	t.Helper()
	old := ringPoll
	ringPoll = 2 * time.Millisecond
	t.Cleanup(func() { ringPoll = old })
}

func TestRingAndAnnounceTriesHandsetsInOrderUntilOneAnswers(t *testing.T) {
	fastPolls(t)
	fake := newFakeAnnouncer(map[string]bool{"office": true})
	targets := []ringTarget{{"kitchen", "PJSIP/kitchen"}, {"office", "PJSIP/office"}, {"attic", "PJSIP/attic"}}
	rep := ringAndAnnounce(context.Background(), fake, targets, "balance", "12", time.Second)
	if rep.Answered != "office" {
		t.Fatalf("answered = %q, want office; report %+v", rep.Answered, rep)
	}
	if strings.Join(rep.Tried, ",") != "kitchen,office" {
		t.Errorf("tried = %v: the attic must not ring once the office answered", rep.Tried)
	}
	for _, a := range fake.args {
		if a != "announce,balance,12" {
			t.Errorf("appArgs = %q — the daemon gets a catalogue entry and a number, nothing else", a)
		}
	}
	if len(fake.hangups) != 0 {
		t.Errorf("hung up %v: the daemon ends an announcement, the CLI only watches", fake.hangups)
	}
}

func TestRingAndAnnounceReportsAnUnplaceableCall(t *testing.T) {
	fastPolls(t)
	fake := newFakeAnnouncer(nil)
	fake.failNext = true
	rep := ringAndAnnounce(context.Background(), fake, []ringTarget{{"kitchen", "PJSIP/kitchen"}}, "balance", "1", time.Second)
	if rep.Error == "" || rep.Answered != "" {
		t.Fatalf("report = %+v, want an error and nobody answered", rep)
	}
}

func TestAnnounceLowRingsOnceADay(t *testing.T) {
	fastPolls(t)
	dir := t.TempDir()
	settings := ringSettings{
		targets:   []ringTarget{{"kitchen", "PJSIP/kitchen"}},
		statePath: filepath.Join(dir, "state", "balance.json"),
		repeat:    24 * time.Hour,
		ringFor:   time.Second,
	}
	results := []balanceResult{{Trunk: "voipms", State: balanceLow, Balance: f64(12.5), Threshold: f64(25)}}
	now := time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC)

	fake := newFakeAnnouncer(map[string]bool{"kitchen": true})
	rep := announceLow(context.Background(), fake, results, settings, now)
	if rep == nil || rep.Answered != "kitchen" || rep.Spoken != 12 || rep.Trunk != "voipms" {
		t.Fatalf("first run: %+v, want the kitchen told about twelve", rep)
	}
	if fi, err := os.Stat(settings.statePath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("state file: %v %v — it records when the house was told, and is nobody else's business", err, fi)
	}

	// An hour later, still low: silence. Alert fatigue is the risk the plan
	// names, and this is the whole of the defence.
	if rep := announceLow(context.Background(), fake, results, settings, now.Add(time.Hour)); rep != nil {
		t.Fatalf("second run within the day rang: %+v", rep)
	}
	// A day later: rings again.
	if rep := announceLow(context.Background(), fake, results, settings, now.Add(25*time.Hour)); rep == nil {
		t.Fatal("a day later the house should be told again")
	}
	// Recovered, then low again next week: rings at once.
	ok := []balanceResult{{Trunk: "voipms", State: balanceOK, Balance: f64(80), Threshold: f64(25)}}
	if rep := announceLow(context.Background(), fake, ok, settings, now.Add(26*time.Hour)); rep != nil {
		t.Fatalf("nothing low, yet it rang: %+v", rep)
	}
	if rep := announceLow(context.Background(), fake, results, settings, now.Add(27*time.Hour)); rep == nil {
		t.Fatal("a fresh dip after a recovery should ring immediately")
	}
}

func TestAnnounceLowDoesNotRecordACallThatNeverWentOut(t *testing.T) {
	fastPolls(t)
	dir := t.TempDir()
	settings := ringSettings{
		targets:   []ringTarget{{"kitchen", "PJSIP/kitchen"}},
		statePath: filepath.Join(dir, "balance.json"),
		repeat:    24 * time.Hour,
		ringFor:   time.Second,
	}
	results := []balanceResult{{Trunk: "voipms", State: balanceLow, Balance: f64(2)}}
	now := time.Now()
	fake := newFakeAnnouncer(nil)
	fake.failNext = true
	if rep := announceLow(context.Background(), fake, results, settings, now); rep == nil || rep.Error == "" {
		t.Fatalf("report = %+v, want the failure named", rep)
	}
	fake.failNext = false
	fake.answers = map[string]bool{"kitchen": true}
	if rep := announceLow(context.Background(), fake, results, settings, now.Add(time.Minute)); rep == nil || rep.Answered != "kitchen" {
		t.Fatalf("after a failed attempt the next run must try again, got %+v", rep)
	}
}

func TestPromTextIsTheExpositionFormatWithoutCredentials(t *testing.T) {
	results := []balanceResult{
		{Trunk: "voipms", Provider: "voip.ms", State: balanceLow, Balance: f64(12.5), Currency: "USD", Threshold: f64(25)},
		{Trunk: "flowroute", Provider: "flowroute", State: balanceNoCapability, Billing: "postpaid"},
		{Trunk: `odd"name`, Provider: "x", State: balanceError, Detail: "TRUNK_X_API_PASSWORD is not set"},
	}
	now := time.Unix(1_790_000_000, 0)
	text := promText(results, now)
	for _, want := range []string{
		`doorman_trunk_balance{trunk="voipms",provider="voip.ms",currency="USD"} 12.5`,
		`doorman_trunk_balance_threshold{trunk="voipms",provider="voip.ms"} 25`,
		`doorman_trunk_balance_known{trunk="flowroute",provider="flowroute"} 0`,
		`doorman_trunk_balance_known{trunk="voipms",provider="voip.ms"} 1`,
		`doorman_trunk_balance_known{trunk="odd\"name",provider="x"} 0`,
		"doorman_balance_last_check_timestamp_seconds 1790000000",
		"# TYPE doorman_trunk_balance gauge",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "flowroute\",provider=\"flowroute\",currency") {
		t.Error("a trunk with no number must not have a balance sample")
	}
	if strings.Contains(text, "API_PASSWORD") || strings.Contains(text, "owner@") {
		t.Error("details and credentials never reach a label")
	}

	path := filepath.Join(t.TempDir(), "doorman_balance.prom")
	if err := writeProm(path, results, now); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != text {
		t.Error("the file must be exactly the text")
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("temp file left behind: %v", entries)
	}
}

func TestBundledMediaPrefixMatchesTheConfigDefault(t *testing.T) {
	// The system phrases come from the bundled pack wherever the lobby's
	// pack is; if the default PROMPT_MEDIA_PREFIX moved, the alert would go
	// silent on every box that never set it.
	src, err := os.ReadFile("../../internal/config/config.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `str("PROMPT_MEDIA_PREFIX", "call-me-maybe")`) {
		t.Fatal("PROMPT_MEDIA_PREFIX default no longer \"call-me-maybe\" — lobby.BundledMediaPrefix must move with it")
	}
}
