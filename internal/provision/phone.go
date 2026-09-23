package provision

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	"callmemaybe/internal/policy"
	"callmemaybe/internal/setup"
)

// Address is where phones reach this box: the LAN address, never localhost,
// never a Tailscale address by default — a phone on the LAN can use nothing
// else. The port is the provisioning window's and the directory's.
type Address struct {
	Host string
	Port int
}

// DefaultPort is where `doorman provision` and the directory listen.
const DefaultPort = 8443

// ParseAddress accepts "192.168.7.133" or "192.168.7.133:8443".
func ParseAddress(s string) (Address, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Address{}, fmt.Errorf("PROVISION_ADDRESS is not set — the LAN address phones reach this box at, e.g. 192.168.7.133")
	}
	host, port := s, DefaultPort
	if h, p, err := net.SplitHostPort(s); err == nil {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return Address{}, fmt.Errorf("PROVISION_ADDRESS %q: bad port", s)
		}
		host, port = h, n
	}
	if host == "" || host == "localhost" || strings.HasPrefix(host, "127.") {
		return Address{}, fmt.Errorf("PROVISION_ADDRESS %q: on the phone, localhost is the phone — use this box's LAN address", s)
	}
	if ip := net.ParseIP(host); ip == nil {
		// A name is allowed only when the phones can resolve it, which is a
		// per-model rehearsal fact; the plan says the IP is the one form that
		// needs no such fact. Names are accepted but the operator is told.
		if strings.HasSuffix(host, ".local") {
			return Address{}, fmt.Errorf("PROVISION_ADDRESS %q: SIP phones resolve through unicast DNS, not mDNS — use the LAN address", s)
		}
	}
	return Address{Host: host, Port: port}, nil
}

// HostPort is host:port, the form a phone's "config server path" takes
// (scheme carried separately, as the vendors want it).
func (a Address) HostPort() string { return net.JoinHostPort(a.Host, strconv.Itoa(a.Port)) }

// BaseURL is what `doorman provision` prints and serves under.
func (a Address) BaseURL() string { return "https://" + a.HostPort() + "/prov/" }

// ConfigServerPath is Grandstream's "Config Server Path": host:port/path,
// no scheme. The phone appends cfg<mac>.xml itself.
func (a Address) ConfigServerPath() string { return a.HostPort() + "/prov" }

// DirectoryPort is where the always-on directory listens: one above the
// window, because the two are different processes with different lifetimes
// and cannot share a socket. Phones poll this one on a timer; the window
// they fetch configuration from is opened by an operator and closes.
func (a Address) DirectoryPort() int { return a.Port + 1 }

// DirectoryHostPort is host:port for the directory.
func (a Address) DirectoryHostPort() string {
	return net.JoinHostPort(a.Host, strconv.Itoa(a.DirectoryPort()))
}

// PhonebookPath is the per-handset directory the phone polls; it appends
// phonebook.xml itself.
func (a Address) PhonebookPath(id string) string { return a.DirectoryHostPort() + "/prov/" + id }

// PhonebookURL is the operator's view of the same thing.
func (a Address) PhonebookURL(id string) string {
	return "https://" + a.PhonebookPath(id) + "/phonebook.xml"
}

// Phone is everything a template needs to know about one handset: the
// inventory row plus the secrets render resolved and the house's address.
type Phone struct {
	ID       string
	Label    string
	MAC      string // canonical aa:bb:cc:dd:ee:ff
	Model    Model
	Number   int
	Page     bool
	Mailbox  string
	Books    []string
	Address  Address
	Timezone string // IANA name; "" when unknown

	SIPPassword       string
	AdminPassword     string
	ProvisionPassword string
}

// VoicemailCode is the feature code every handset's voicemail key dials.
// It is what extensions.conf binds; the registry (s14) will own it.
const VoicemailCode = "*97"

// DisplayName is the idle-screen label: "Kitchen-101" — the room and its own
// number, so the person holding the phone knows both.
func (p Phone) DisplayName() string {
	if p.Number > 0 {
		return fmt.Sprintf("%s-%d", p.Label, p.Number)
	}
	return p.Label
}

// FileName is the file the phone will ask for. Grandstream firmware requests
// cfg<mac>.xml with the MAC as twelve lower-case hex digits.
func (p Phone) FileName() string {
	switch p.Model.Family {
	case "grandstream-xml":
		return "cfg" + policy.MACFilename(p.MAC) + ".xml"
	case "yealink-cfg":
		return policy.MACFilename(p.MAC) + ".cfg"
	}
	return ""
}

// Render writes the phone's configuration in its vendor's format.
func Render(p Phone) ([]byte, error) {
	switch p.Model.Family {
	case "grandstream-xml":
		return renderGrandstream(p)
	}
	return nil, fmt.Errorf("model %s has no template yet", p.Model.ID)
}

// Env resolves a secret by name — the same shape render uses.
type Env func(key string) (string, bool)

// Built is the outcome of BuildAll: files by name, and the handsets that were
// named but could not be rendered, with the reason a person can act on.
type Built struct {
	Files map[string][]byte
	// Phones lists what was rendered, sorted by id, for the session to
	// print instructions from.
	Phones []Phone
	Notes  []string
}

// BuildAll renders a configuration file for every handset that carries mac
// and model. A handset without them is not an error — it registers by hand
// — and a known model without a template is reported, not failed. A missing
// secret or address is an error, exactly as a missing password_env is for
// the PJSIP fragments: nothing half-rendered reaches disk.
func BuildAll(handsets []policy.Handset, env Env, timezone string) (*Built, error) {
	out := &Built{Files: map[string][]byte{}}
	var problems []string
	fail := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	var addr Address
	addrResolved := false
	for _, h := range handsets {
		if h.MAC == "" || h.Model == "" {
			continue
		}
		m, ok := Lookup(h.Model)
		if !ok {
			fail("handset %q model %q is not a model doorman knows", h.ID, h.Model)
			continue
		}
		if !m.Templated {
			out.Notes = append(out.Notes, fmt.Sprintf("handset %q: %s has no template yet — it registers by hand", h.ID, m.ID))
			continue
		}
		if !addrResolved {
			raw, _ := env("PROVISION_ADDRESS")
			a, err := ParseAddress(raw)
			if err != nil {
				return nil, fmt.Errorf("provisioning: %v", err)
			}
			addr, addrResolved = a, true
		}
		mac, ok := policy.NormaliseMAC(h.MAC)
		if !ok {
			fail("handset %q mac %q is not a MAC address", h.ID, h.MAC)
			continue
		}
		secret := func(name, what string) string {
			v, ok := env(name)
			if !ok || v == "" {
				fail("handset %q: %s is not set — %s; `doorman init` generates it", h.ID, name, what)
			}
			return v
		}
		p := Phone{
			ID:                h.ID,
			Label:             firstNonEmpty(h.Label, h.ID),
			MAC:               mac,
			Model:             m,
			Number:            h.Number,
			Page:              h.Page,
			Mailbox:           h.Mailbox,
			Books:             h.Books(),
			Address:           addr,
			Timezone:          timezone,
			SIPPassword:       secret(h.PasswordEnv, "the SIP password password_env names"),
			AdminPassword:     secret(setup.AdminEnvVarFor(h.ID), "the phone's web-admin password"),
			ProvisionPassword: secret(setup.ProvisionEnvVarFor(h.ID), "the credential the phone presents to fetch its configuration"),
		}
		if h.PasswordEnv == "" {
			fail("handset %q needs password_env", h.ID)
		}
		body, err := Render(p)
		if err != nil {
			fail("handset %q: %v", h.ID, err)
			continue
		}
		out.Files[p.FileName()] = body
		out.Phones = append(out.Phones, p)
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("provisioning: %s", strings.Join(problems, "; "))
	}
	sort.Slice(out.Phones, func(i, j int) bool { return out.Phones[i].ID < out.Phones[j].ID })
	return out, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// posixTZ maps an IANA zone to the POSIX string phones want. Unknown zones
// leave the phone's own setting alone rather than guess.
var posixTZ = map[string]string{
	"UTC":                 "UTC0",
	"Etc/UTC":             "UTC0",
	"America/New_York":    "EST5EDT,M3.2.0,M11.1.0",
	"America/Chicago":     "CST6CDT,M3.2.0,M11.1.0",
	"America/Denver":      "MST7MDT,M3.2.0,M11.1.0",
	"America/Phoenix":     "MST7",
	"America/Los_Angeles": "PST8PDT,M3.2.0,M11.1.0",
	"America/Anchorage":   "AKST9AKDT,M3.2.0,M11.1.0",
	"Pacific/Honolulu":    "HST10",
	"America/Toronto":     "EST5EDT,M3.2.0,M11.1.0",
	"America/Vancouver":   "PST8PDT,M3.2.0,M11.1.0",
	"Europe/London":       "GMT0BST,M3.5.0/1,M10.5.0",
	"Europe/Dublin":       "GMT0IST,M3.5.0/1,M10.5.0",
	"Europe/Paris":        "CET-1CEST,M3.5.0,M10.5.0/3",
	"Europe/Berlin":       "CET-1CEST,M3.5.0,M10.5.0/3",
	"Europe/Amsterdam":    "CET-1CEST,M3.5.0,M10.5.0/3",
	"Australia/Sydney":    "AEST-10AEDT,M10.1.0,M4.1.0/3",
}

// POSIXTimezone returns the phone-side zone string, or "" when unknown.
func POSIXTimezone(iana string) string { return posixTZ[iana] }
