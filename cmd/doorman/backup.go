package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"callmemaybe/internal/awssig"
	"callmemaybe/internal/backup"
	"callmemaybe/internal/events"
	"callmemaybe/internal/setup"
)

// backupOpts is everything `backup` and `restore` read from flags and .env,
// gathered once so the two commands and `check` agree on what the house is.
type backupOpts struct {
	envPath, handsets, policy, trunks, contacts, messages string
	stateDir, journal, voicemail, spool, asteriskDir      string
	noVoicemail                                           bool
	env                                                   func(string) (string, bool)
	from                                                  string // which destination verify/restore read: "" = the first
}

// pick returns the destination named by -from, or the first.
func (o *backupOpts) pick(dests []backup.Destination) (backup.Destination, bool) {
	if o.from == "" {
		return dests[0], true
	}
	for _, d := range dests {
		if d.Name() == o.from {
			return d, true
		}
	}
	return nil, false
}

func (o *backupOpts) sources() backup.Sources {
	return backup.Sources{
		Config:    []string{o.envPath, o.policy, o.handsets, o.trunks, o.contacts, o.messages},
		StateDir:  o.stateDir,
		Journal:   o.journal,
		Voicemail: o.voicemail,
		Spool:     o.spool,
		// The hand-written Asterisk files. The "only where it differs from
		// what shipped" rule arrives with the embedded templates (s09 M1);
		// until then every one of them is in — small, and never wrong.
		Asterisk: func() []string {
			var out []string
			for _, f := range []string{"ari.conf", "pjsip.conf", "extensions.conf", "voicemail.conf", "modules.conf", "cel.conf", "cel_sqlite3_custom.conf", "pjsip_notify.conf", "http.conf", "rtp.conf"} {
				out = append(out, filepath.Join(o.asteriskDir, f))
			}
			return out
		}(),
		NoVoicemail: o.noVoicemail,
		Exclude:     o.excludeDirs(),
	}
}

// excludeDirs keeps a file destination out of its own bundles, and the
// backup state directory (last.json, the on-box key copy) out of every one.
func (o *backupOpts) excludeDirs() []string {
	out := []string{filepath.Join(o.stateDir, "backup")}
	if p, ok := o.env("BACKUP_PATH"); ok && strings.TrimSpace(p) != "" {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

// destinations is every backend .env names. The file backend is here; s3
// and the account are s27 M4 and later, and plug in at this one place.
func (o *backupOpts) destinations() ([]backup.Destination, []string) {
	var dests []backup.Destination
	var missing []string
	if p, ok := o.env("BACKUP_PATH"); ok && strings.TrimSpace(p) != "" {
		dests = append(dests, backup.FileDest{Dir: strings.TrimSpace(p)})
	}
	if b, ok := o.env("BACKUP_S3_BUCKET"); ok && strings.TrimSpace(b) != "" {
		get := func(k string) string { v, _ := o.env(k); return strings.TrimSpace(v) }
		switch {
		case get("BACKUP_S3_ENDPOINT") == "":
			missing = append(missing, "BACKUP_S3_BUCKET is set but BACKUP_S3_ENDPOINT is not")
		case get("BACKUP_S3_KEY_ID") == "" || get("BACKUP_S3_SECRET") == "":
			missing = append(missing, "BACKUP_S3_BUCKET is set but BACKUP_S3_KEY_ID / BACKUP_S3_SECRET are not")
		default:
			dests = append(dests, backup.S3Dest{Endpoint: get("BACKUP_S3_ENDPOINT"), Bucket: strings.TrimSpace(b), Region: get("BACKUP_S3_REGION"),
				Prefix: get("BACKUP_S3_PREFIX"), Creds: awssig.Credentials{AccessKeyID: get("BACKUP_S3_KEY_ID"), SecretAccessKey: get("BACKUP_S3_SECRET")}})
		}
	}
	if t, ok := o.env("BACKUP_CLOUD_TOKEN"); ok && strings.TrimSpace(t) != "" {
		missing = append(missing, "BACKUP_CLOUD_TOKEN is set but the account destination does not exist yet")
	}
	return dests, missing
}

func (o *backupOpts) retention() (daily, weekly int) {
	daily, weekly = 7, 8
	if v, ok := o.env("BACKUP_KEEP_DAILY"); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			daily = n
		}
	}
	if v, ok := o.env("BACKUP_KEEP_WEEKLY"); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
			weekly = n
		}
	}
	return
}

func backupFlags(fs *flag.FlagSet) (*backupOpts, func()) {
	o := &backupOpts{}
	envFlag := fs.String("env", "./.env", "secrets file (BACKUP_* keys, and the first file in every bundle)")
	handsetsFlag := fs.String("handsets", "", "inventory file (default $HANDSETS_PATH or ./handsets.toml)")
	policyFlag := fs.String("policy", "", "policy file (default $POLICY_PATH or ./policy.toml)")
	trunksFlag := fs.String("trunks", "", "provider inventory (default $TRUNKS_PATH or ./trunks.toml)")
	contactsFlag := fs.String("contacts", "", "address-book inventory (default $CONTACTS_PATH or ./contacts.toml)")
	messagesFlag := fs.String("messages", "", "words file (default $MESSAGES_PATH or ./messages.toml)")
	stateFlag := fs.String("state", "/var/lib/doorman", "doorman's state directory")
	voicemailFlag := fs.String("voicemail", "/var/spool/asterisk/voicemail", "Asterisk's voicemail spool")
	asteriskFlag := fs.String("asterisk", "/etc/asterisk", "Asterisk's configuration directory")
	noVM := fs.Bool("no-voicemail", false, "leave the voicemail messages out (they are the bulk)")
	fromFlag := fs.String("from", "", "which destination verify and restore read: file or s3 (default: the first configured)")
	return o, func() {
		o.from = strings.TrimSpace(*fromFlag)
		o.envPath = *envFlag
		o.env = secretLookup(*envFlag)
		o.handsets, o.policy = handsetsPathArg(*handsetsFlag), policyPathArg(*policyFlag)
		o.trunks, o.contacts, o.messages = trunksPathArg(*trunksFlag), contactsPathArg(*contactsFlag), messagesPathArg(*messagesFlag)
		o.stateDir, o.voicemail, o.asteriskDir, o.noVoicemail = *stateFlag, *voicemailFlag, *asteriskFlag, *noVM
		if j, ok := o.env("EVENT_JOURNAL_PATH"); ok && strings.TrimSpace(j) != "" {
			o.journal = strings.TrimSpace(j)
		}
		o.spool = "/var/spool/call-me-maybe"
		if s, ok := o.env("PHONEBOOK_SPOOL"); ok && strings.TrimSpace(s) != "" {
			o.spool = strings.TrimSpace(s)
		}
	}
}

// runBackup is `doorman backup init|run|list|verify`.
func runBackup(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: doorman backup init|run|list|verify [flags]")
		return 2
	}
	verb, rest := args[0], args[1:]
	fs := flag.NewFlagSet("backup "+verb, flag.ExitOnError)
	o, resolve := backupFlags(fs)
	_ = fs.Parse(rest)
	resolve()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	switch verb {
	case "init":
		return backupInit(o)
	case "run":
		return backupRun(ctx, o)
	case "list":
		return backupList(ctx, o)
	case "verify":
		return backupVerify(ctx, o, fs.Args())
	}
	fmt.Fprintf(os.Stderr, "✗ unknown verb %q; want init, run, list or verify\n", verb)
	return 2
}

// backupInit makes the keypair: the recipient into .env, the identity
// printed once and never stored. Refuses to replace an existing recipient
// without --force, because every bundle made so far would need the old
// identity and the operator may not know that.
func backupInit(o *backupOpts) int {
	if r, ok := o.env("BACKUP_RECIPIENT"); ok && strings.TrimSpace(r) != "" {
		fmt.Fprintln(os.Stderr, "✗ BACKUP_RECIPIENT is already set. A new key would leave every existing bundle readable only with the old identity.")
		fmt.Fprintln(os.Stderr, "  To rotate on purpose: remove the line from .env, keep the old identity somewhere, run init again.")
		return 1
	}
	recipient, identity, err := backup.NewKey()
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 1
	}
	if err := setup.RotateSecrets(o.envPath, []string{"BACKUP_RECIPIENT"}, func() (string, error) { return recipient, nil }); err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 1
	}
	fmt.Println("✓ BACKUP_RECIPIENT written to", o.envPath, "— the box encrypts with it and can never decrypt.")
	fmt.Println()
	fmt.Println("This is the private key. It is printed once and stored nowhere on this box.")
	fmt.Println("Keep it where you keep the house's master keys: a password manager, a")
	fmt.Println("printed sheet in the safe. Without it, every bundle is a brick.")
	fmt.Println()
	fmt.Println("  " + identity)
	fmt.Println()
	fmt.Println("Next: a destination (BACKUP_PATH=/some/dir in .env), then `doorman backup run`,")
	fmt.Println("then `sudo doorman init services` so the nightly timer is enabled.")
	return 0
}

type lastBackup struct {
	At       time.Time `json:"at"`
	Name     string    `json:"name"`
	Dests    []string  `json:"dests"`
	Failed   []string  `json:"failed,omitempty"`
	Files    int       `json:"files"`
	Bytes    int64     `json:"bytes"`
	Doorman  string    `json:"doorman"`
	Verified bool      `json:"verified"`
}

func lastBackupPath(stateDir string) string { return filepath.Join(stateDir, "backup", "last.json") }

func backupRun(ctx context.Context, o *backupOpts) int {
	recipient, ok := o.env("BACKUP_RECIPIENT")
	if !ok || strings.TrimSpace(recipient) == "" {
		fmt.Println("backup: BACKUP_RECIPIENT is not set, so there is no key to encrypt to. `doorman backup init` makes one. Nothing done.")
		return 0
	}
	dests, notYet := o.destinations()
	for _, n := range notYet {
		fmt.Println("backup: " + n)
	}
	if len(dests) == 0 {
		fmt.Println("backup: no destination is set (BACKUP_PATH). Nothing done.")
		return 0
	}
	host, _ := os.Hostname()
	now := time.Now()
	items, m, err := backup.Collect(o.sources(), version, host, now)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 1
	}
	var buf bytes.Buffer
	if err := backup.Bundle(&buf, m, items, recipient); err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 1
	}
	name := backup.Name(host, now)
	var journal *events.Sibling
	if o.journal != "" {
		journal = events.OpenSibling(o.journal, events.Options{})
		defer journal.Close()
	}
	last := lastBackup{At: now, Name: name, Files: len(m.Files), Bytes: int64(buf.Len()), Doorman: version}

	// The on-box key, when the operator keeps one. A key that is
	// configured but cannot be read is a failed run: verification was
	// asked for and did not happen. No key at all is the quiet default.
	identity, err := identityFrom(o.stateDir)
	if err != nil {
		fmt.Printf("✗ verify: %v — a configured key that cannot be read fails the run\n", err)
		last.Failed = append(last.Failed, "verify")
		post(ctx, journal, events.BackupFailed, "verify")
		writeLast(o.stateDir, last)
		return 1
	}
	// The first proof comes BEFORE delivery: a bundle that does not open
	// with the key is not a backup, and must not replace — or prune — the
	// good one already at every destination.
	if identity != "" {
		if _, err := backup.ReadManifest(bytes.NewReader(buf.Bytes()), identity); err != nil {
			fmt.Printf("✗ verify: the bundle does not open with the on-box key: %v — not delivered\n", err)
			last.Failed = append(last.Failed, "verify")
			post(ctx, journal, events.BackupFailed, "verify")
			writeLast(o.stateDir, last)
			return 1
		}
	}

	daily, weekly := o.retention()
	results := backup.Deliver(ctx, dests, name, buf.Bytes(), daily, weekly, now)
	rc := 0
	verified := identity != ""
	for i, r := range results {
		if r.Err != nil {
			rc = 1
			last.Failed = append(last.Failed, r.Dest)
			fmt.Printf("✗ %s: %v\n", r.Dest, r.Err)
			post(ctx, journal, events.BackupFailed, r.Dest)
			continue
		}
		// "Verified" means what the destination RETAINED opens: read the
		// object back and decrypt it, not the buffer that was sent. A
		// destination that acknowledged a PUT and kept a truncated object
		// is a failed destination, whatever it answered.
		if identity != "" {
			if err := readBack(ctx, dests[i], name, identity, len(m.Files)); err != nil {
				rc = 1
				verified = false
				last.Failed = append(last.Failed, r.Dest)
				fmt.Printf("✗ %s: delivered, but what it kept does not open: %v\n", r.Dest, err)
				post(ctx, journal, events.BackupFailed, r.Dest)
				continue
			}
		}
		last.Dests = append(last.Dests, r.Dest)
		fmt.Printf("✓ %s: %s (%d files, %s", r.Dest, name, len(m.Files), humanBytes(int64(buf.Len())))
		if r.Pruned > 0 {
			fmt.Printf("; pruned %d", r.Pruned)
		}
		if identity != "" {
			fmt.Print("; read back and opened")
		}
		fmt.Println(")")
		post(ctx, journal, events.BackupCompleted, r.Dest)
	}
	last.Verified = verified && len(last.Dests) > 0
	if last.Verified {
		fmt.Println("✓ verified: every destination's copy was read back and opened with the on-box key")
	} else if identity == "" {
		fmt.Println("  (not verified: no on-box key copy at " + identityFile(o.stateDir) + ")")
	}
	writeLast(o.stateDir, last)
	return rc
}

// readBack fetches the object a destination just stored and opens it with
// the identity, checking the manifest lists what was sent.
func readBack(ctx context.Context, d backup.Destination, name, identity string, files int) error {
	rc, err := d.Get(ctx, name)
	if err != nil {
		return err
	}
	defer rc.Close()
	m, err := backup.ReadManifest(rc, identity)
	if err != nil {
		return err
	}
	if len(m.Files) != files {
		return fmt.Errorf("manifest lists %d files, %d were sent", len(m.Files), files)
	}
	return nil
}

func writeLast(stateDir string, last lastBackup) {
	if b, err := json.MarshalIndent(last, "", "  "); err == nil {
		_ = os.MkdirAll(filepath.Dir(lastBackupPath(stateDir)), 0o700)
		_ = os.WriteFile(lastBackupPath(stateDir), b, 0o600)
	}
}

func post(ctx context.Context, j *events.Sibling, t events.Type, dest string) {
	if j == nil {
		return
	}
	e := events.System(t, dest, 0)
	e.Source = "doorman-backup"
	if err := j.Append(ctx, e); err != nil && !errors.Is(err, events.ErrNoJournal) {
		fmt.Printf("  journal: %v\n", err)
	}
}

func backupList(ctx context.Context, o *backupOpts) int {
	dests, _ := o.destinations()
	if len(dests) == 0 {
		fmt.Println("backup: no destination is set (BACKUP_PATH).")
		return 0
	}
	for _, d := range dests {
		names, err := d.List(ctx)
		if err != nil {
			fmt.Printf("%s: %v\n", d.Name(), err)
			continue
		}
		fmt.Printf("%s: %d bundle(s)\n", d.Name(), len(names))
		for _, n := range names {
			fmt.Printf("  %s\n", n)
		}
	}
	return 0
}

// backupVerify opens the newest (or named) bundle with the identity and
// prints its manifest: proof the key still opens what the box writes.
func backupVerify(ctx context.Context, o *backupOpts, args []string) int {
	identity, ok := readIdentity(o.stateDir)
	if !ok {
		return 2
	}
	dests, _ := o.destinations()
	if len(dests) == 0 {
		fmt.Println("backup: no destination is set (BACKUP_PATH).")
		return 2
	}
	name := ""
	if len(args) > 0 {
		name = args[0]
	}
	d, ok := o.pick(dests)
	if !ok {
		fmt.Fprintf(os.Stderr, "✗ no destination named %q is configured\n", o.from)
		return 2
	}
	if name == "" {
		names, err := d.List(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "✗ %v\n", err)
			return 1
		}
		latest, found := backup.Latest(names)
		if !found {
			fmt.Println("backup: no bundles at", d.Name())
			return 1
		}
		name = latest
	}
	rc, err := d.Get(ctx, name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 1
	}
	defer rc.Close()
	m, err := backup.ReadManifest(rc, identity)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %s: %v\n", name, err)
		return 1
	}
	fmt.Printf("✓ %s at %s opens: %s\n", name, d.Name(), m.Summary())
	for _, e := range m.Excluded {
		fmt.Printf("    excluded: %s\n", e)
	}
	return 0
}

// identityFile is where the on-box copy of the private key lives when the
// operator keeps one: beside last.json, in the one directory under the
// state tree that is never bundled. The authoritative copy is the
// operator's, off the box; this one lets the box prove nightly that what
// it wrote opens.
func identityFile(stateDir string) string { return filepath.Join(stateDir, "backup", "identity.key") }

// identityFrom returns the private key from BACKUP_IDENTITY_FILE, else the
// on-box copy if there is one. "" means neither exists.
func identityFrom(stateDir string) (string, error) {
	p := strings.TrimSpace(os.Getenv("BACKUP_IDENTITY_FILE"))
	if p == "" {
		p = identityFile(stateDir)
		if _, err := os.Stat(p); err != nil {
			return "", nil
		}
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// readIdentity takes the private key from BACKUP_IDENTITY_FILE, the on-box
// copy, or asks on the terminal. Never from .env, never from an argument
// (shells remember).
func readIdentity(stateDir string) (string, bool) {
	if id, err := identityFrom(stateDir); err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return "", false
	} else if id != "" {
		return id, true
	}
	fmt.Fprint(os.Stderr, "Private key (AGE-SECRET-KEY-…): ")
	var line string
	if _, err := fmt.Fscanln(os.Stdin, &line); err != nil || !strings.HasPrefix(line, "AGE-SECRET-KEY-") {
		fmt.Fprintln(os.Stderr, "✗ that is not an age identity; set BACKUP_IDENTITY_FILE or type it")
		return "", false
	}
	return line, true
}

// runRestore is `doorman restore [bundle] [--from file] [--latest]`: the
// box from a bundle. Refuses a box that already has a .env unless --force,
// and then backs up first. Never while the daemon is running — the journal
// snapshot replaces a live database.
func runRestore(args []string) int {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	o, resolve := backupFlags(fs)
	dryRun := fs.Bool("dry-run", false, "show the manifest and what would be written; change nothing")
	force := fs.Bool("force", false, "restore onto a box that already has config (it is backed up first)")
	latest := fs.Bool("latest", false, "the newest bundle at the destination")
	root := fs.String("root", "", "write under this directory instead of / (for rehearsals)")
	_ = fs.Parse(args)
	resolve()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	var src io.ReadCloser
	var name string
	switch {
	case fs.NArg() == 1 && !*latest:
		name = fs.Arg(0)
		f, err := os.Open(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "✗ %v\n", err)
			return 1
		}
		src = f
	case *latest:
		dests, _ := o.destinations()
		if len(dests) == 0 {
			fmt.Fprintln(os.Stderr, "✗ --latest needs a destination (BACKUP_PATH)")
			return 2
		}
		d, ok := o.pick(dests)
		if !ok {
			fmt.Fprintf(os.Stderr, "✗ no destination named %q is configured\n", o.from)
			return 2
		}
		names, err := d.List(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "✗ %v\n", err)
			return 1
		}
		n, ok := backup.Latest(names)
		if !ok {
			fmt.Fprintln(os.Stderr, "✗ no bundles at the destination")
			return 1
		}
		rc, err := d.Get(ctx, n)
		if err != nil {
			fmt.Fprintf(os.Stderr, "✗ %v\n", err)
			return 1
		}
		src, name = rc, n
	default:
		fmt.Fprintln(os.Stderr, "usage: doorman restore <bundle.age> | --latest [--dry-run] [--force] [--root DIR]")
		return 2
	}
	defer src.Close()
	identity, ok := readIdentity(o.stateDir)
	if !ok {
		return 2
	}
	m, items, err := backup.Open(src, identity)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %s: %v\n", name, err)
		return 1
	}
	fmt.Printf("Bundle: %s\n  %s\n", name, m.Summary())
	for _, e := range m.Excluded {
		fmt.Printf("  excluded: %s\n", e)
	}
	if *dryRun {
		fmt.Println("Would write:")
		for _, it := range items {
			fmt.Printf("  %-9s %s\n", it.Tier, filepath.Join(*root, it.Path))
		}
		return 0
	}
	// "Already has a house" is judged by the bundle's own targets — the
	// absolute paths it would write — never by whatever is in the current
	// directory. Any house-tier file present means a house is here.
	var present []string
	for _, it := range items {
		if it.Tier != "house" {
			continue
		}
		if _, err := os.Stat(filepath.Join(*root, it.Path)); err == nil {
			present = append(present, filepath.Join(*root, it.Path))
		}
	}
	if len(present) > 0 && !*force {
		fmt.Fprintf(os.Stderr, "✗ this box already has a house: %s exists. --force restores over it (every file it replaces is backed up first); --dry-run shows what would change.\n", present[0])
		return 1
	}
	if *root == "" {
		if out, err := os.ReadFile("/run/systemd/units/invocation:doorman.service"); err == nil && len(out) > 0 {
			fmt.Fprintln(os.Stderr, "✗ doorman is running: stop it first (`sudo systemctl stop doorman`), the journal is replaced by a snapshot.")
			return 1
		}
	}
	if *force {
		// Every file the bundle will replace is kept beside itself first —
		// not just .env — so a restore can be undone file by file.
		kept := 0
		for _, it := range items {
			target := filepath.Join(*root, it.Path)
			if _, err := os.Stat(target); err != nil {
				continue
			}
			if _, err := setup.Backup(target); err != nil {
				fmt.Fprintf(os.Stderr, "✗ could not keep a copy of %s: %v\n", target, err)
				return 1
			}
			kept++
		}
		fmt.Printf("  %d existing file(s) kept beside themselves as .bak-<time>\n", kept)
	}
	written, unowned, err := backup.Place(items, *root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ after %d file(s): %v\n", len(written), err)
		return 1
	}
	for _, u := range unowned {
		fmt.Printf("  ! owner not restored: %s (create the user, then chown)\n", u)
	}
	if os.Geteuid() != 0 && *root == "" {
		fmt.Println("  ! not root: owners were not restored; files belong to you. Run as root for a real restore.")
	}
	fmt.Printf("✓ %d file(s) restored from %s (doorman %s, %s)\n", len(written), m.Host, m.Doorman, m.CreatedAt.Format(time.RFC3339))
	if *root != "" {
		fmt.Println("Laid out under", *root, "— nothing started, nothing registered. Inspect and diff at leisure.")
		return 0
	}
	fmt.Println("Next: `doorman check`, `doorman render` and the reloads it prints, `sudo doorman init services`, then `sudo systemctl start doorman`.")
	fmt.Println()
	fmt.Println("If the box this came from is still running, do NOT start Asterisk or the inbox here:")
	fmt.Println("  the trunk in pjsip.conf registers one sub-account, and the provider keeps the last")
	fmt.Println("  registration — inbound calls would flip between the two boxes; and two inboxes on one")
	fmt.Println("  token would split the house's texts. Phones are unaffected: they stay on the box whose")
	fmt.Println("  window they fetched from. Change PROVISION_ADDRESS in .env if this box has a new IP.")
	return 0
}

// describeBackup is check's line: the last run, its age, and where it went.
func describeBackup(stateDir string, now time.Time, env func(string) (string, bool)) (string, bool) {
	if r, ok := env("BACKUP_RECIPIENT"); !ok || strings.TrimSpace(r) == "" {
		return "Backup: not set up (`doorman backup init`, then BACKUP_PATH in .env)", true
	}
	b, err := os.ReadFile(lastBackupPath(stateDir))
	if err != nil {
		return "Backup: key set, no run yet (`doorman backup run`; the timer runs nightly once `init services` enables it)", false
	}
	var last lastBackup
	if json.Unmarshal(b, &last) != nil {
		return "Backup: last.json unreadable", false
	}
	age := now.Sub(last.At).Round(time.Minute)
	line := fmt.Sprintf("Backup: last %s ago → %s (%d files, %s)", age, strings.Join(last.Dests, ", "), last.Files, humanBytes(last.Bytes))
	ok := true
	if last.Verified {
		line += ", verified (read back)"
	} else if len(last.Failed) == 0 {
		line += ", not verified (no on-box key: " + identityFile(stateDir) + ")"
	}
	if p := strings.TrimSpace(os.Getenv("BACKUP_IDENTITY_FILE")); p != "" {
		if abs, err := filepath.Abs(p); err == nil && !strings.HasPrefix(abs, filepath.Join(stateDir, "backup")+string(filepath.Separator)) &&
			(strings.HasPrefix(abs, stateDir+string(filepath.Separator)) || strings.HasPrefix(abs, "/opt/call-me-maybe/")) {
			line += " — ! BACKUP_IDENTITY_FILE lies where bundles are collected; move it to " + identityFile(stateDir)
			ok = false
		}
	}
	if len(last.Failed) > 0 {
		line += " — failed: " + strings.Join(last.Failed, ", ")
		ok = false
	}
	if age > 26*time.Hour {
		line += " — STALE, over a day"
		ok = false
	}
	return line, ok
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
