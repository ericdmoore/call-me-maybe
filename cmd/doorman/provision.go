package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"callmemaybe/internal/ari"
	"callmemaybe/internal/policy"
	"callmemaybe/internal/provision"
	provserve "callmemaybe/internal/provision/serve"
	"callmemaybe/internal/xdg"
)

// `doorman provision` — a phone configures itself, and the operator watches
// it happen.
//
// The failure this replaces is a person typing a SIP password into a phone's
// web page, one phone at a time, and getting one character wrong on the
// third phone. Here the inventory carries the phone's MAC and model, render
// writes the file the phone will ask for by name, and this command opens a
// bounded HTTPS window on the LAN, tells the operator exactly what to type,
// serves the file to the listed MAC and nobody else, and reports the phone's
// registration as Asterisk sees it. No arguments is the inventory view: no
// listener, nothing changes.
//
// This is the only file in cmd/doorman that may name the serving package,
// asserted by test the way `balance` guards the provider package: a LAN
// listener that hands out SIP passwords must never share a process with the
// thing that answers the phone.
//
// Exit codes:
//
//	0  every named phone fetched its configuration and registered
//	1  the window closed (or Ctrl-C) with at least one named phone missing
//	2  the command line, the inventory, or the environment was wrong

const (
	provisionExitMissing = 1
	provisionExitUsage   = 2
	defaultWindow        = 15 * time.Minute
)

func runProvision(args []string) int {
	if len(args) > 0 && args[0] == "directory" {
		return runProvisionDirectory(args[1:])
	}
	notify := len(args) > 0 && args[0] == "notify"
	if notify {
		args = args[1:]
	}
	fs := flag.NewFlagSet("provision", flag.ExitOnError)
	handsetsFlag := fs.String("handsets", "", "inventory file (default $HANDSETS_PATH or ./handsets.toml)")
	envFlag := fs.String("env", "./.env", "secrets file for handset passwords and PROVISION_ADDRESS")
	outFlag := fs.String("out", "./asterisk/generated", "where render writes; the files are served from <out>/provisioning")
	stateFlag := fs.String("state", "", "where the certificate and first-contact records live (default $XDG_STATE_HOME/doorman/provision)")
	windowFlag := fs.Duration("window", defaultWindow, "how long the window stays open")
	forever := fs.Bool("forever", false, "keep the window open until Ctrl-C (warns: every listed phone can fetch unauthenticated the whole time)")
	all := fs.Bool("all", false, "every handset with a mac and model")
	models := fs.Bool("models", false, "list the model ids handsets.toml accepts")
	exportCert := fs.Bool("export-cert", false, "print the window's certificate (PEM) for a phone that validates servers")
	_ = fs.Parse(args)

	if *models {
		printModels(os.Stdout)
		return 0
	}

	handsetsPath := handsetsPathArg(*handsetsFlag)
	handsets, _, err := policy.LoadHandsets(handsetsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return provisionExitUsage
	}
	env := secretLookup(*envFlag)
	built, err := provision.BuildAll(handsets, provision.Env(env), hostTimezone())
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return provisionExitUsage
	}
	stateDir := provisionStateDir(*stateFlag)
	provDir := filepath.Join(*outFlag, "provisioning")

	if len(built.Phones) == 0 {
		fmt.Printf("No handset in %s carries a mac and model, so there is nothing to provision.\n", handsetsPath)
		fmt.Println("Add `mac = \"…\"` and `model = \"…\"` to a [[handsets]] block; `doorman provision --models` lists the models.")
		for _, note := range built.Notes {
			fmt.Printf("  · %s\n", note)
		}
		return 0
	}
	address := built.Phones[0].Address

	// Ctrl-C closes the window cleanly: the context is cancelled, the
	// listener shuts down, and the session reports what it saw.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	reader := provisionARIReader(ctx, env)

	// The inventory view.
	if fs.NArg() == 0 && !*all && !*exportCert && !notify {
		srv, err := provserve.New(provserve.Options{Dir: provDir, StateDir: stateDir, Phones: built.Phones, Address: address})
		if err != nil {
			fmt.Fprintf(os.Stderr, "✗ %v\n", err)
			return provisionExitUsage
		}
		printInventory(ctx, os.Stdout, handsets, built, srv, reader)
		return 0
	}

	// A session: which phones.
	var named []provision.Phone
	if *all {
		named = built.Phones
	} else {
		byID := map[string]provision.Phone{}
		for _, p := range built.Phones {
			byID[p.ID] = p
		}
		for _, id := range fs.Args() {
			p, ok := byID[id]
			if !ok {
				fmt.Fprintf(os.Stderr, "✗ %q is not a handset with a mac and model in %s\n", id, handsetsPath)
				fmt.Fprintln(os.Stderr, "  `doorman provision` alone lists what can be provisioned.")
				return provisionExitUsage
			}
			named = append(named, p)
		}
	}

	// Render first, every time: the files under provisioning/ are outputs of
	// the inventory and the secrets, and a session that served a stale one
	// would be a phone registering on last month's password.
	if err := os.MkdirAll(provDir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return provisionExitUsage
	}
	for name, body := range built.Files {
		if err := os.WriteFile(filepath.Join(provDir, name), body, 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "✗ %v\n", err)
			return provisionExitUsage
		}
	}
	for _, p := range named {
		fmt.Printf("→ rendered %s (%s)\n", filepath.Join(provDir, p.FileName()), p.ID)
	}

	events := make(chan provserve.Event, 64)
	srv, err := provserve.New(provserve.Options{
		Dir: provDir, StateDir: stateDir, Phones: built.Phones, Address: address,
		OnEvent: func(e provserve.Event) {
			select {
			case events <- e:
			default:
			}
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return provisionExitUsage
	}
	if *exportCert {
		pemBytes, err := srv.CertificatePEM()
		if err != nil {
			fmt.Fprintf(os.Stderr, "✗ %v\n", err)
			return provisionExitUsage
		}
		os.Stdout.Write(pemBytes)
		return 0
	}

	window := *windowFlag
	if *forever {
		window = 0
		fmt.Fprintln(os.Stderr, "! --forever: every listed phone can fetch its configuration unauthenticated until you press Ctrl-C")
	}
	session := &provisionSession{
		phones: named, server: srv, reader: reader, window: window, poll: 2 * time.Second,
		events: events, out: os.Stdout,
		listen: func(ctx context.Context) error { return srv.ListenAndServe(ctx, window) },
	}
	if notify {
		if *all && len(named) == 0 {
			fmt.Fprintln(os.Stderr, "✗ nothing to notify")
			return provisionExitUsage
		}
		session.notify = asteriskNotify
	}
	return session.run(ctx)
}

// asteriskNotify tells a registered phone to fetch its configuration again,
// through the Asterisk console — there is no ARI call for a NOTIFY, and the
// console is already how the runbook reloads. Needs the pjsip_notify.conf
// this repo ships, which defines check-sync.
func asteriskNotify(id string) (string, error) {
	out, err := exec.Command("asterisk", "-rx", "pjsip send notify check-sync endpoint "+id).CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		return text, fmt.Errorf("asterisk -rx failed — run as a user in the asterisk group, or with sudo: %s", text)
	}
	if strings.Contains(text, "Unable to find") || strings.Contains(text, "not found") {
		return text, errors.New(text)
	}
	return text, nil
}

// provisionStateDir is where the certificate and first-contact records
// live. Under the service user it is /var/lib/doorman/provision; for an
// operator running the CLI it is their own state home, so nothing here
// needs root.
func provisionStateDir(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if p := os.Getenv("PROVISION_STATE_DIR"); p != "" {
		return p
	}
	return filepath.Join(xdg.Dir("STATE", os.Getenv, os.UserHomeDir), "doorman", "provision")
}

// endpointReader is the one ARI question this command asks: is the phone
// registered. Read-only, over loopback, the same client `check` pings with.
type endpointReader interface {
	Endpoint(ctx context.Context, tech, resource string) (ari.EndpointState, error)
}

// provisionARIReader returns nil when ARI is not configured or not
// answering; the session then watches fetches only and says so.
func provisionARIReader(ctx context.Context, env func(string) (string, bool)) endpointReader {
	user, okU := env("ARI_USERNAME")
	pass, okP := env("ARI_PASSWORD")
	if !okU || !okP {
		return nil
	}
	base, _ := env("ARI_BASE_URL")
	if base == "" {
		base = "http://127.0.0.1:8088"
	}
	app, _ := env("ARI_APP")
	if app == "" {
		app = "doorman"
	}
	client := ari.New(ari.Options{BaseURL: base, Username: user, Password: pass, App: app})
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if _, err := client.Ping(pingCtx); err != nil {
		return nil
	}
	return client
}

// ── the inventory view ───────────────────────────────────────────────────

func printModels(w io.Writer) {
	fmt.Fprintln(w, "Models handsets.toml accepts as `model = \"…\"`:")
	for _, m := range provision.Models() {
		how := "configures itself"
		if !m.Templated {
			how = "listed; no template yet — set up by hand"
		}
		fmt.Fprintf(w, "  %-22s %s %s — %s\n", m.ID, m.Vendor, m.Name, how)
	}
}

type registration struct {
	known  bool
	online bool
}

func readRegistration(ctx context.Context, r endpointReader, id string) registration {
	if r == nil {
		return registration{}
	}
	c, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	st, err := r.Endpoint(c, "PJSIP", id)
	if err != nil {
		return registration{}
	}
	return registration{known: true, online: strings.EqualFold(st.State, "online")}
}

func printInventory(ctx context.Context, w io.Writer, handsets []policy.Handset, built *provision.Built, srv *provserve.Server, r endpointReader) {
	address := built.Phones[0].Address
	fmt.Fprintf(w, "  %s        window closed — `doorman provision <id…>` opens one\n\n", address.BaseURL())
	if r == nil {
		fmt.Fprintln(w, "  (Asterisk is not answering over ARI from here, so registration is not shown)")
		fmt.Fprintln(w)
	}
	byID := map[string]provision.Phone{}
	for _, p := range built.Phones {
		byID[p.ID] = p
	}
	for _, h := range handsets {
		if !strings.HasPrefix(h.Endpoint, "PJSIP/") {
			continue
		}
		p, ok := byID[h.ID]
		if !ok {
			why := "no mac — manual path"
			if h.MAC != "" {
				why = "no template for " + h.Model + " — manual path"
			}
			fmt.Fprintf(w, "  %-12s %-10s %-19s %-22s %s\n", h.ID, modelShort(h.Model), "—", "—", why)
			continue
		}
		state := provisionState(ctx, p, srv, r)
		fmt.Fprintf(w, "  %-12s %-10s %-19s %-22s %s\n", p.ID, modelShort(p.Model.ID), p.MAC, p.FileName(), state)
	}
	fmt.Fprintln(w)
	if len(built.Phones) > 0 {
		fmt.Fprintf(w, "  inspect:  %s%s.xml   (asks for that phone's provisioning login)\n", address.BaseURL(), built.Phones[0].ID)
	}
	for _, note := range built.Notes {
		fmt.Fprintf(w, "  · %s\n", note)
	}
}

func provisionState(ctx context.Context, p provision.Phone, srv *provserve.Server, r endpointReader) string {
	reg := readRegistration(ctx, r, p.ID)
	at, seen := srv.SeenAt(p.MAC)
	switch {
	case reg.online && seen:
		return fmt.Sprintf("registered   provisioned %s", at.Format("2006-01-02 15:04"))
	case reg.online:
		return "registered   (by hand — never fetched a configuration)"
	case seen && reg.known:
		return fmt.Sprintf("provisioned %s   offline", at.Format("2006-01-02 15:04"))
	case seen:
		return fmt.Sprintf("provisioned %s", at.Format("2006-01-02 15:04"))
	case reg.known:
		return "never provisioned   offline"
	default:
		return "never provisioned"
	}
}

func modelShort(id string) string {
	if id == "" {
		return "—"
	}
	if m, ok := provision.Lookup(id); ok {
		return strings.ToUpper(strings.TrimPrefix(m.ID, strings.ToLower(m.Vendor)+"-"))
	}
	return id
}

// ── the session ──────────────────────────────────────────────────────────

type provisionSession struct {
	phones []provision.Phone
	server *provserve.Server
	// notify, when set, is sent to each phone once the window is open: the
	// re-provisioning path, where the phone is already registered and needs
	// telling that its configuration changed.
	notify func(id string) (string, error)
	reader endpointReader // nil: fetch-only watch
	window time.Duration  // 0: until cancelled
	poll   time.Duration
	events <-chan provserve.Event
	out    io.Writer
	listen func(ctx context.Context) error
}

type phoneProgress struct {
	fetched    bool
	registered bool
}

func (s *provisionSession) run(ctx context.Context) int {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	if s.window > 0 {
		fmt.Fprintf(s.out, "→ serving %s  for %s  (Ctrl-C to close)\n\n", s.phones[0].Address.BaseURL(), s.window)
	} else {
		fmt.Fprintf(s.out, "→ serving %s  until Ctrl-C\n\n", s.phones[0].Address.BaseURL())
	}
	for _, p := range s.phones {
		printInstructions(s.out, p)
	}
	if s.reader == nil {
		fmt.Fprintln(s.out, "  (Asterisk is not answering over ARI from here — watching fetches only; registration is not confirmed)")
	}
	fmt.Fprintf(s.out, "  waiting for %s …\n", joinIDs(s.phones))

	listenErr := make(chan error, 1)
	go func() { listenErr <- s.listen(ctx) }()

	if s.notify != nil {
		// The listener binds before the phone can possibly answer a NOTIFY,
		// but give it the head start anyway.
		time.Sleep(200 * time.Millisecond)
		for _, p := range s.phones {
			if _, err := s.notify(p.ID); err != nil {
				fmt.Fprintf(s.out, "  %s  %-10s notify failed: %v\n", time.Now().Format("15:04:05"), p.ID, err)
				continue
			}
			fmt.Fprintf(s.out, "  %s  %-10s sent check-sync — the phone should fetch within seconds\n", time.Now().Format("15:04:05"), p.ID)
		}
	}

	progress := map[string]*phoneProgress{}
	for _, p := range s.phones {
		progress[p.ID] = &phoneProgress{}
	}
	done := func() bool {
		for _, p := range s.phones {
			pr := progress[p.ID]
			if !pr.fetched || (s.reader != nil && !pr.registered) {
				return false
			}
		}
		return true
	}
	stamp := func() string { return time.Now().Format("15:04:05") }

	var deadline <-chan time.Time
	if s.window > 0 {
		t := time.NewTimer(s.window)
		defer t.Stop()
		deadline = t.C
	}
	poll := time.NewTicker(s.poll)
	defer poll.Stop()

	finish := func(reason string) int {
		cancel()
		<-listenErr
		var missing []string
		for _, p := range s.phones {
			pr := progress[p.ID]
			if !pr.fetched || (s.reader != nil && !pr.registered) {
				missing = append(missing, p.ID)
			}
		}
		if len(missing) == 0 {
			fmt.Fprintf(s.out, "✓ %d of %d handsets registered — window closed\n", len(s.phones), len(s.phones))
			return 0
		}
		fmt.Fprintf(s.out, "✗ %d of %d handsets registered — %s; still missing: %s\n",
			len(s.phones)-len(missing), len(s.phones), reason, strings.Join(missing, ", "))
		return provisionExitMissing
	}

	for {
		select {
		case err := <-listenErr:
			if err != nil {
				fmt.Fprintf(s.out, "✗ listener: %v\n", err)
				listenErr <- nil
				return finish("listener failed")
			}
			listenErr <- nil
			return finish("listener closed")
		case <-ctx.Done():
			return finish("window closed")
		case <-deadline:
			return finish("window closed")
		case e := <-s.events:
			s.printEvent(stamp(), e)
			if pr, ok := progress[e.Handset]; ok && e.Kind == "fetched" {
				pr.fetched = true
			}
			if done() {
				return finish("done")
			}
		case <-poll.C:
			for _, p := range s.phones {
				pr := progress[p.ID]
				if !pr.fetched || pr.registered || s.reader == nil {
					continue
				}
				if readRegistration(ctx, s.reader, p.ID).online {
					pr.registered = true
					fmt.Fprintf(s.out, "  %s  %-10s registered  PJSIP/%s\n", stamp(), p.ID, p.ID)
				}
			}
			if done() {
				return finish("done")
			}
		}
	}
}

func (s *provisionSession) printEvent(at string, e provserve.Event) {
	switch e.Kind {
	case "connected":
		fmt.Fprintf(s.out, "  %s  %-10s connected from %s\n", at, e.Handset, e.Remote)
	case "fetched":
		fmt.Fprintf(s.out, "  %s  %-10s fetched %s\n", at, e.Handset, e.Detail)
	case "refused":
		fmt.Fprintf(s.out, "  %s  !          %s\n", at, e.Detail)
	case "unauthorized":
		fmt.Fprintf(s.out, "  %s  %-10s refused: %s\n", at, e.Handset, e.Detail)
	case "error":
		fmt.Fprintf(s.out, "  %s  %-10s error: %s\n", at, e.Handset, e.Detail)
	case "phonebook":
		fmt.Fprintf(s.out, "  %s  %-10s phonebook fetched (%s)\n", at, e.Handset, e.Detail)
	case "inspected":
		fmt.Fprintf(s.out, "  %s  %-10s inspected %s from %s\n", at, e.Handset, e.Detail, e.Remote)
	}
}

// printInstructions is what to type or press, per model family. The
// wording is corrected against the phone's own menus at each rehearsal —
// the plan says so — and nothing here is a password or a phone number.
func printInstructions(w io.Writer, p provision.Phone) {
	fmt.Fprintf(w, "  %s — %s %s, %s\n", p.ID, p.Model.Vendor, p.Model.Name, p.MAC)
	switch p.Model.Family {
	case "grandstream-xml":
		if strings.HasPrefix(p.Model.ID, "grandstream-wp") {
			// The WP8xx handset menu has no config-server entry (Settings →
			// Zero Config is Grandstream's cloud, not this); the web UI is
			// the path. The handset shows its IP under Settings → Status.
			fmt.Fprintf(w, "    on the phone:  (Wi-Fi first: Settings → Quick Network Configuration; then Settings → Status for its IP)\n")
		} else {
			fmt.Fprintf(w, "    on the phone:  Menu → System → Provisioning → Config Server Path: %s   Upgrade via: HTTPS\n", p.Address.ConfigServerPath())
		}
		fmt.Fprintf(w, "    on the web:    https://<phone-ip> (admin, the password on the sticker) → Maintenance → Upgrade and Provisioning → Config File\n")
		fmt.Fprintf(w, "                   Config Upgrade Via: HTTPS   Config Server Path: %s\n", p.Address.ConfigServerPath())
		fmt.Fprintf(w, "                   Save and Apply, then Provision Now (or reboot)\n")
	default:
		fmt.Fprintf(w, "    point the phone's provisioning server at %s (HTTPS)\n", p.Address.BaseURL())
	}
	fmt.Fprintf(w, "    router:        DHCP option 66 = %s\n", strings.TrimSuffix(p.Address.BaseURL(), "/"))
	fmt.Fprintf(w, "                   (then nothing on the phone at all)\n\n")
}

func joinIDs(phones []provision.Phone) string {
	ids := make([]string, 0, len(phones))
	for _, p := range phones {
		ids = append(ids, p.ID)
	}
	sort.Strings(ids)
	return strings.Join(ids, ", ")
}

// ── the directory ────────────────────────────────────────────────────────

// runProvisionDirectory is the always-on process: phone books over HTTPS
// with each phone's credential, one port above the window, and never a
// configuration. It re-reads the inventory, the policy and the contacts
// when they change, so a name added to [[people]] is on every phone at its
// next poll with no render and no session. With no PROVISION_ADDRESS it
// exits 0 — a house of hand-configured phones has nothing to serve.
func runProvisionDirectory(args []string) int {
	fs := flag.NewFlagSet("provision directory", flag.ExitOnError)
	handsetsFlag := fs.String("handsets", "", "inventory file (default $HANDSETS_PATH or ./handsets.toml)")
	policyFlag := fs.String("policy", "", "policy file, for [[people]] (default $POLICY_PATH or ./policy.toml)")
	contactsFlag := fs.String("contacts", "", "address-book inventory, optional (default $CONTACTS_PATH or ./contacts.toml)")
	envFlag := fs.String("env", "./.env", "secrets file")
	stateFlag := fs.String("state", "", "certificate directory (default $XDG_STATE_HOME/doorman/provision)")
	_ = fs.Parse(args)

	env := secretLookup(*envFlag)
	if raw, ok := env("PROVISION_ADDRESS"); !ok || strings.TrimSpace(raw) == "" {
		fmt.Println("PROVISION_ADDRESS is not set: no phone fetches a directory from this box, so there is none to serve.")
		return 0
	}
	paths := bookPaths{handsets: handsetsPathArg(*handsetsFlag), policy: policyPathArg(*policyFlag),
		contacts: contactsPathArg(*contactsFlag), countryCode: defaultCountryCode()}
	handsets, _, err := policy.LoadHandsets(paths.handsets)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return provisionExitUsage
	}
	built, err := provision.BuildAll(handsets, provision.Env(env), hostTimezone())
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return provisionExitUsage
	}
	if len(built.Phones) == 0 {
		fmt.Println("No handset carries a mac and model, so no phone polls a directory. Nothing to serve.")
		return 0
	}
	cache := &bookCache{paths: paths}
	if _, err := cache.current(); err != nil {
		fmt.Fprintf(os.Stderr, "✗ phone books: %v\n", err)
		return provisionExitUsage
	}
	byID := map[string]policy.Handset{}
	for _, h := range handsets {
		byID[h.ID] = h
	}
	models := map[string]provision.Model{}
	for _, p := range built.Phones {
		models[p.ID] = p.Model
	}
	address := built.Phones[0].Address
	address.Port = address.DirectoryPort()
	srv, err := provserve.New(provserve.Options{
		StateDir: provisionStateDir(*stateFlag), Phones: built.Phones, Address: address, Directory: true,
		Phonebook: func(id string) ([]byte, error) {
			books, err := cache.current()
			if err != nil {
				return nil, err
			}
			return renderBooks(byID[id], models[id], books)
		},
		OnEvent: func(e provserve.Event) {
			// Journal lines: a handset id and an address, never a name.
			switch e.Kind {
			case "phonebook":
				fmt.Printf("%s phonebook %s from %s\n", e.Time.Format(time.RFC3339), e.Handset, e.Remote)
			case "refused", "error":
				fmt.Printf("%s %s %s\n", e.Time.Format(time.RFC3339), e.Kind, e.Detail)
			}
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return provisionExitUsage
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	fmt.Printf("directory: serving %d phone book(s) on https://%s/prov/<id>/phonebook.xml\n", len(built.Phones), address.HostPort())
	if err := srv.ListenAndServe(ctx, 0); err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 1
	}
	return 0
}
