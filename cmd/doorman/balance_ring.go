package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"callmemaybe/internal/ari"
	"callmemaybe/internal/lobby"
	"callmemaybe/internal/policy"
)

// `doorman balance --ring kitchen` — the alert is a phone call.
//
// The obvious design is email, and the obvious objection to that is that
// when the balance is zero you cannot afford to be told. Both are wrong,
// because an internal call never touches a trunk: this originates over ARI
// to a handset on the LAN, and the daemon says "the calling credit is down
// to about" and a number. No provider, no credit, no registration. A dead
// account can still ring the kitchen to say it is dead.
//
// It is also not new machinery. Originating to a handset and playing a
// prompt is what this program does; the only thing that had to be built is
// the daemon knowing how to read a number aloud (lobby.Announcement). The
// provider API key stays exactly where M1 put it — in this command and
// nowhere near the daemon — because all the daemon receives is
// announce,balance,<number> as Stasis arguments.
//
// Two consequences worth stating. The ring needs the daemon running and
// reachable on loopback, so a run that rings is a run on the box, and the
// key comes with it; RUNBOOK says what that costs and how to scope the key.
// And it must never become a nuisance, so a trunk rings once per --repeat
// (a day) and the fact is kept in a small state file, not in memory that a
// cron job would lose between runs.

// ringTarget is one handset to try, in the order given.
type ringTarget struct {
	ID       string
	Endpoint string
}

// ringTargets resolves handset and group ids from handsets.toml to the PJSIP
// endpoints an announcement can ring, in the order written. Groups expand in
// place; a pseudo-handset (Local/…, a conference) is skipped with a note
// rather than an error, because "ring the adults" should not fail the day a
// pseudo-handset joins the group.
func ringTargets(ids []string, handsets []policy.Handset, groups []policy.Group) ([]ringTarget, []string, error) {
	byID := make(map[string]policy.Handset, len(handsets))
	for _, h := range handsets {
		byID[h.ID] = h
	}
	byGroup := make(map[string]policy.Group, len(groups))
	for _, g := range groups {
		byGroup[g.ID] = g
	}
	var out []ringTarget
	var skipped []string
	seen := map[string]bool{}
	add := func(id string) error {
		h, ok := byID[id]
		if !ok {
			return fmt.Errorf("%q is not a handset or group id in handsets.toml", id)
		}
		if seen[id] {
			return nil
		}
		seen[id] = true
		if !strings.HasPrefix(h.Endpoint, "PJSIP/") {
			skipped = append(skipped, id)
			return nil
		}
		out = append(out, ringTarget{ID: id, Endpoint: h.Endpoint})
		return nil
	}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if g, ok := byGroup[id]; ok {
			for _, member := range g.Handsets {
				if err := add(member); err != nil {
					return nil, nil, err
				}
			}
			continue
		}
		if err := add(id); err != nil {
			return nil, nil, err
		}
	}
	if len(out) == 0 {
		return nil, skipped, errors.New("nothing to ring: no PJSIP handset among the ids given")
	}
	return out, skipped, nil
}

// spokenBalance is what the announcement reads: the lowest balance among the
// trunks below their threshold, as a whole number, rounded down so "about
// twelve" is never more than the account holds. With several trunks low it
// is the worst of them, because the trunk names cannot be spoken without
// recording them and the person hearing it will look at the table.
func spokenBalance(results []balanceResult) (trunk string, whole int, ok bool) {
	lowest := math.Inf(1)
	for _, r := range results {
		if r.State != balanceLow || r.Balance == nil {
			continue
		}
		if *r.Balance < lowest {
			lowest, trunk, ok = *r.Balance, r.Trunk, true
		}
	}
	if !ok {
		return "", 0, false
	}
	if lowest < 0 {
		lowest = 0
	}
	return trunk, int(math.Floor(lowest)), true
}

// ── Repeat suppression ───────────────────────────────────────────────────

// ringState is the one fact this command keeps between runs: when each low
// trunk was last announced. A trunk that is no longer low is forgotten, so
// the next time it dips it rings straight away.
type ringState struct {
	Rang map[string]ringRecord `json:"rang"`
}

type ringRecord struct {
	At      time.Time `json:"at"`
	Balance float64   `json:"balance"`
}

func loadRingState(path string) (ringState, error) {
	st := ringState{Rang: map[string]ringRecord{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		// A corrupt state file must not stop the alert; it means one extra
		// ring, which is the safe direction to fail in.
		return ringState{Rang: map[string]ringRecord{}}, nil
	}
	if st.Rang == nil {
		st.Rang = map[string]ringRecord{}
	}
	return st, nil
}

func (st ringState) save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data, 0o600)
}

// dueToRing says which low trunks have not been announced within repeat, and
// forgets every trunk that is no longer low. Zero repeat means every run
// rings, which is what --repeat 0 asks for and nothing else does.
func dueToRing(st *ringState, results []balanceResult, now time.Time, repeat time.Duration) []string {
	low := map[string]bool{}
	var due []string
	for _, r := range results {
		if r.State != balanceLow {
			continue
		}
		low[r.Trunk] = true
		last, rang := st.Rang[r.Trunk]
		if !rang || repeat <= 0 || now.Sub(last.At) >= repeat {
			due = append(due, r.Trunk)
		}
	}
	for trunk := range st.Rang {
		if !low[trunk] {
			delete(st.Rang, trunk)
		}
	}
	sort.Strings(due)
	return due
}

// ── The call ─────────────────────────────────────────────────────────────

// announcer is the slice of the ARI client the ring needs, so the tests can
// drive it with a fake and no Asterisk.
type announcer interface {
	Originate(ctx context.Context, p ari.OriginateParams) (string, error)
	Channel(ctx context.Context, channelID string) (ari.Channel, error)
	Hangup(ctx context.Context, channelID string) error
}

// ringReport is what happened, for the table and for --json.
type ringReport struct {
	Trunk    string   `json:"trunk"`
	Spoken   int      `json:"spoken"`
	Answered string   `json:"answered,omitempty"` // the handset that picked up
	Tried    []string `json:"tried"`
	Error    string   `json:"error,omitempty"`
}

// ringPoll is how often the channel is read while it rings. Coarse on
// purpose: this is a phone ringing for half a minute, not a hot loop. A
// variable only so the tests can shorten it.
var ringPoll = 500 * time.Millisecond

// ringAndAnnounce rings each target in turn until one answers, and returns
// once the announcement on that handset has ended. An unanswered handset
// ends when Asterisk's ring timeout expires; the next is tried. Nothing here
// waits on a handset forever: every wait has the ring timeout plus the
// announcement's own bound behind it.
func ringAndAnnounce(ctx context.Context, client announcer, targets []ringTarget, kind, value string, ringFor time.Duration) ringReport {
	rep := ringReport{}
	args := strings.Join([]string{lobby.AnnounceArg, kind, value}, ",")
	for _, t := range targets {
		rep.Tried = append(rep.Tried, t.ID)
		// Ring, then the announcement, then a margin; past that the channel
		// is hung up rather than trusted to end on its own.
		deadline := ringFor + 90*time.Second
		callCtx, cancel := context.WithTimeout(ctx, deadline)
		id, err := client.Originate(callCtx, ari.OriginateParams{
			Endpoint: t.Endpoint,
			AppArgs:  args,
			CallerID: "Call Me Maybe <balance>",
			Timeout:  int(math.Ceil(ringFor.Seconds())),
		})
		if err != nil {
			cancel()
			rep.Error = fmt.Sprintf("could not ring %s: %v", t.ID, err)
			return rep
		}
		answered := watchChannel(callCtx, client, id)
		cancel()
		if answered {
			rep.Answered = t.ID
			return rep
		}
	}
	return rep
}

// watchChannel polls one channel until it is gone and reports whether it was
// ever up — which for an originated channel means the handset answered. On
// ctx expiry the channel is hung up on a fresh context, so a call this
// command started never outlives the command.
func watchChannel(ctx context.Context, client announcer, channelID string) (answered bool) {
	tick := time.NewTicker(ringPoll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			hctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = client.Hangup(hctx, channelID)
			cancel()
			return answered
		case <-tick.C:
			ch, err := client.Channel(ctx, channelID)
			if ari.IsNotFound(err) {
				return answered
			}
			if err != nil {
				continue // a transient read failure is not the call ending
			}
			if ch.State == "Up" {
				answered = true
			}
		}
	}
}

// ringSettings is everything the ring needs from flags and the environment,
// resolved once in runBalance so the work below is a function of its inputs.
type ringSettings struct {
	targets   []ringTarget
	skipped   []string
	statePath string
	repeat    time.Duration
	ringFor   time.Duration
}

// announceLow is the whole feature end to end: decide, ring, remember.
// Returns nil when nothing was due. The exit code is not its business —
// a low balance is exit 1 whether or not the kitchen picked up.
func announceLow(ctx context.Context, client announcer, results []balanceResult, s ringSettings, now time.Time) *ringReport {
	st, err := loadRingState(s.statePath)
	if err != nil {
		return &ringReport{Error: fmt.Sprintf("reading %s: %v", s.statePath, err)}
	}
	due := dueToRing(&st, results, now, s.repeat)
	// Save even when nothing is due: dueToRing may have forgotten a trunk
	// that recovered, and that is worth writing down.
	if len(due) == 0 {
		_ = st.save(s.statePath)
		return nil
	}
	trunk, whole, ok := spokenBalance(results)
	if !ok {
		return nil
	}
	rep := ringAndAnnounce(ctx, client, s.targets, lobby.AnnounceKindBalance, strconv.Itoa(whole), s.ringFor)
	rep.Trunk, rep.Spoken = trunk, whole
	// Every due trunk counts as announced by this one call, whether or not
	// anybody answered: a house that lets it ring out has been told as much
	// as a phone can tell it, and ringing again in an hour helps nobody.
	// A failure to place the call at all is different, and is not recorded,
	// so the next run tries again.
	if rep.Error == "" {
		for _, t := range due {
			bal := 0.0
			for _, r := range results {
				if r.Trunk == t && r.Balance != nil {
					bal = *r.Balance
				}
			}
			st.Rang[t] = ringRecord{At: now, Balance: bal}
		}
		if err := st.save(s.statePath); err != nil {
			rep.Error = fmt.Sprintf("announced, but could not write %s: %v", s.statePath, err)
		}
	}
	return &rep
}

func printRingReport(rep *ringReport, s ringSettings) {
	if rep == nil {
		return
	}
	fmt.Println()
	switch {
	case rep.Error != "" && rep.Answered == "":
		fmt.Printf("✗ the alert call did not go out: %s\n", rep.Error)
		fmt.Println("  The daemon has to be running on this box for the kitchen to ring:")
		fmt.Println("  the announcement is an internal call over ARI, which binds to loopback.")
	case rep.Answered != "":
		fmt.Printf("☎ %s answered and was told the %s balance is down to about %d.\n",
			rep.Answered, rep.Trunk, rep.Spoken)
	default:
		fmt.Printf("☎ rang %s about the %s balance; nobody answered.\n",
			strings.Join(rep.Tried, ", "), rep.Trunk)
	}
	if rep.Error != "" && rep.Answered != "" {
		fmt.Printf("  note: %s\n", rep.Error)
	}
	if len(s.skipped) > 0 {
		fmt.Printf("  note: %s cannot be rung — not a PJSIP handset\n", strings.Join(s.skipped, ", "))
	}
	if s.repeat > 0 {
		fmt.Printf("  It will not ring about the same trunk again for %s (--repeat).\n", s.repeat)
	}
}

// writeFileAtomic writes to a sibling temp file and renames, so a reader
// never sees a half-written file. Shared by the ring state and the
// Prometheus textfile, which node_exporter reads at any moment it likes.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}
