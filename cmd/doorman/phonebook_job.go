package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"callmemaybe/internal/events"
	"callmemaybe/internal/ownbook"
	"callmemaybe/internal/policy"
	"callmemaybe/internal/render"
	"callmemaybe/internal/stt"
)

// spoolName is "<handset>-<unix seconds>-<digits>.wav", exactly as the *88
// dialplan writes it: the handset id is the endpoint name, the time is
// Asterisk's EPOCH, the digits are what Read collected.
var spoolName = regexp.MustCompile(`^([a-z0-9][a-z0-9_-]*)-(\d{9,11})-(\d{3,15})\.wav$`)

// giveUpAfter is how long a recording waits for a transcription before the
// number keeps its number for a name and the audio is dropped.
const giveUpAfter = 7 * 24 * time.Hour

// phonebookJob is one run of `doorman phonebook`: the spool in, books out.
// Deps are plain values so the job runs in a test with a temp dir, a fake
// transcriber and a fixed clock, exactly as the keeper does.
type phonebookJob struct {
	dir         string // PHONEBOOK_DIR: own/<id>.vcf
	spool       string // PHONEBOOK_SPOOL: what *88 recorded
	handsets    map[string]policy.Handset
	countryCode string
	stt         stt.Transcriber // nil: file numbers, leave recordings
	journal     interface {
		Append(context.Context, events.Event) error
	}
	now func() time.Time
	out func(format string, args ...any)
}

func runPhonebook(args []string) int {
	fs := flag.NewFlagSet("phonebook", flag.ExitOnError)
	handsetsFlag := fs.String("handsets", "", "inventory file (default $HANDSETS_PATH or ./handsets.toml)")
	envFlag := fs.String("env", "./.env", "secrets file, for PHONEBOOK_DIR and STT_*")
	_ = fs.Parse(args)

	env := secretLookup(*envFlag)
	handsets, _, err := policy.LoadHandsets(handsetsPathArg(*handsetsFlag))
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 1
	}
	byID := map[string]policy.Handset{}
	for _, h := range handsets {
		byID[h.ID] = h
	}
	job := &phonebookJob{
		dir: render.PhonebookDir(render.Env(env)), spool: render.PhonebookSpool(render.Env(env)),
		handsets: byID, countryCode: defaultCountryCode(),
		now: time.Now, out: func(f string, a ...any) { fmt.Printf(f+"\n", a...) },
	}
	if ep, ok := env("STT_ENDPOINT"); ok && strings.TrimSpace(ep) != "" {
		w := &stt.Whisper{Endpoint: strings.TrimSpace(ep)}
		if m, ok := env("STT_MODEL"); ok {
			w.Model = strings.TrimSpace(m)
		}
		if ms, ok := env("STT_TIMEOUT_MS"); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(ms)); err == nil && n > 0 {
				w.Timeout = time.Duration(n) * time.Millisecond
			}
		}
		job.stt = w
	}
	if p, ok := env("EVENT_JOURNAL_PATH"); ok && strings.TrimSpace(p) != "" {
		j := events.OpenSibling(strings.TrimSpace(p), events.Options{})
		defer j.Close()
		job.journal = j
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := job.run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 1
	}
	return 0
}

// run files every recording in the spool, names what it can, and leaves
// the rest for next time. One bad file never stops the others.
func (j *phonebookJob) run(ctx context.Context) error {
	names, err := filepath.Glob(filepath.Join(j.spool, "*.wav"))
	if err != nil {
		return err
	}
	for _, path := range names {
		j.one(ctx, path)
	}
	return nil
}

func (j *phonebookJob) one(ctx context.Context, path string) {
	base := filepath.Base(path)
	m := spoolName.FindStringSubmatch(base)
	if m == nil {
		j.out("phonebook: %s is not a *88 recording, leaving it", base)
		return
	}
	id, stamp, digits := m[1], m[2], m[3]
	if _, ok := j.handsets[id]; !ok {
		j.out("phonebook: %s names no handset in the inventory, leaving it", base)
		return
	}
	secs, _ := strconv.ParseInt(stamp, 10, 64)
	added := time.Unix(secs, 0)
	n := policy.NormaliseCallerID(digits, j.countryCode)
	if n.Kind != policy.KindE164 {
		// Not a number anyone can dial: nothing to file, nothing to keep.
		j.out("phonebook: %s keyed something that is not a phone number; dropped", id)
		_ = os.Remove(path)
		return
	}
	uid := id + "-" + stamp

	ownDir := filepath.Join(j.dir, "own")
	book, err := ownbook.Load(ownDir, id)
	if err != nil {
		j.out("phonebook: %v", err)
		return
	}
	if book.Upsert(ownbook.Entry{UID: uid, Name: displayNumber(n.Value), E164: n.Value, Added: added}) {
		if err := ownbook.Save(ownDir, book); err != nil {
			j.out("phonebook: %v", err)
			return
		}
		j.out("phonebook: %s filed a number", id)
		j.post(ctx, events.PhonebookAdded, id)
	}

	// The name. The entry the recording belongs to is found by uid; a
	// repeat filing of a number already named keeps the first name and
	// drops this audio unheard.
	entry, ok := book.Find(uid)
	if !ok {
		_ = os.Remove(path)
		return
	}
	if entry.Named {
		_ = os.Remove(path)
		return
	}
	if j.stt == nil {
		return // keeps its number for a name until a service is configured
	}
	audio, err := os.ReadFile(path)
	if err != nil {
		j.out("phonebook: %v", err)
		return
	}
	text, err := j.stt.Transcribe(ctx, base, audio)
	if err != nil {
		if j.now().Sub(added) > giveUpAfter {
			j.out("phonebook: %s's recording went %d days without a transcription; keeping the number, dropping the audio", id, int(giveUpAfter.Hours()/24))
			_ = os.Remove(path)
			return
		}
		j.out("phonebook: transcription for %s failed, will retry: %v", id, err)
		return
	}
	name := cleanName(text)
	if name == "" {
		j.out("phonebook: transcription for %s heard nothing usable, will retry", id)
		return
	}
	if book.Rename(uid, name) {
		if err := ownbook.Save(ownDir, book); err != nil {
			j.out("phonebook: %v", err)
			return
		}
	}
	_ = os.Remove(path)
	j.out("phonebook: %s named a number", id)
	j.post(ctx, events.PhonebookNamed, id)
}

func (j *phonebookJob) post(ctx context.Context, t events.Type, handset string) {
	if j.journal == nil {
		return
	}
	e := events.System(t, handset, 0)
	e.Source = "doorman-phonebook"
	if err := j.journal.Append(ctx, e); err != nil && !errors.Is(err, events.ErrNoJournal) {
		j.out("phonebook: journal: %v", err)
	}
}

// displayNumber is the placeholder name: the number as a person would
// write it, "(972) 555-0142" for NANP, the E.164 form for anything else.
func displayNumber(e164 string) string {
	if len(e164) == 12 && strings.HasPrefix(e164, "+1") {
		d := e164[2:]
		return "(" + d[:3] + ") " + d[3:6] + "-" + d[6:]
	}
	return e164
}

// cleanName is a transcript as a phone-book name: one line, trailing
// full stop gone, at most 40 characters, and nothing when nothing is left.
func cleanName(text string) string {
	name := strings.Join(strings.Fields(text), " ")
	name = strings.TrimRight(name, ".!?,; ")
	if len(name) > 40 {
		name = strings.TrimSpace(name[:40])
	}
	return name
}
