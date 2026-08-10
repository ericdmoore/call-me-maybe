package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const twoContactSources = `
[[sources]]
id = "eric"
path = "/var/lib/doorman/contacts/eric.vcf"

[[sources]]
id = "nuisance"
path = "/var/lib/doorman/contacts/blocked.vcf"
kind = "block"
`

func mustContacts(t *testing.T, body string) *Contacts {
	t.Helper()
	c, err := ContactsFromTOML([]byte(body))
	if err != nil {
		t.Fatalf("ContactsFromTOML: %v", err)
	}
	return c
}

func contactsError(t *testing.T, body string) string {
	t.Helper()
	_, err := ContactsFromTOML([]byte(body))
	if err == nil {
		t.Fatal("expected an error")
	}
	return err.Error()
}

func TestContactSourcesKeepDeclarationOrder(t *testing.T) {
	// The only ordering there is: it decides which of two address books names a
	// shared number, so "which one wins" has to be answerable by reading the
	// file top to bottom.
	c := mustContacts(t, twoContactSources)
	if c.Len() != 2 {
		t.Fatalf("sources = %d, want 2", c.Len())
	}
	if c.All()[0].ID != "eric" || c.All()[1].ID != "nuisance" {
		t.Errorf("order = %q, %q", c.All()[0].ID, c.All()[1].ID)
	}
	if got := c.IDs(); len(got) != 2 || got[0] != "eric" {
		t.Errorf("IDs() = %v", got)
	}
}

func TestContactSourceKindDefaultsToAdmit(t *testing.T) {
	c := mustContacts(t, twoContactSources)
	if k := c.All()[0].Kind; k != ContactAdmit {
		t.Errorf("kind = %q, want %q", k, ContactAdmit)
	}
	if k := c.All()[1].Kind; k != ContactBlock {
		t.Errorf("kind = %q, want %q", k, ContactBlock)
	}
}

func TestContactSourceNeedsExactlyOnePlaceToReadFrom(t *testing.T) {
	if e := contactsError(t, "[[sources]]\nid = \"eric\"\n"); !strings.Contains(e, "needs a path") {
		t.Errorf("error = %q", e)
	}
	both := "[[sources]]\nid = \"eric\"\npath = \"/tmp/a.vcf\"\nurl = \"https://example.com/a.vcf\"\n"
	if e := contactsError(t, both); !strings.Contains(e, "both path and url") {
		t.Errorf("error = %q", e)
	}
}

func TestContactSourceURLMustBeOne(t *testing.T) {
	for _, bad := range []string{"example.com/a.vcf", "ftp://example.com/a.vcf", "https://"} {
		body := "[[sources]]\nid = \"eric\"\nurl = \"" + bad + "\"\n"
		if e := contactsError(t, body); !strings.Contains(e, "http or https") {
			t.Errorf("url %q: error = %q", bad, e)
		}
	}
}

// The rule worth being strict about in a file that must never hold a secret:
// a token pasted into token_env fails the load rather than reaching a commit.
func TestContactTokenEnvNamesAVariableAndNeverHoldsOne(t *testing.T) {
	body := "[[sources]]\nid = \"eric\"\nurl = \"https://example.com/a.vcf\"\ntoken_env = \"sk-live-abc123\"\n"
	e := contactsError(t, body)
	if !strings.Contains(e, "NAME of a .env variable") {
		t.Errorf("error = %q", e)
	}
	if !strings.Contains(e, "CONTACTS_ERIC_TOKEN") {
		t.Errorf("error does not suggest the convention: %q", e)
	}
	// And it never echoes back anything but what was written, so a real token
	// pasted here is not repeated anywhere it could be copied on from.
	if strings.Count(e, "sk-live-abc123") != 1 {
		t.Errorf("the offending value appears %d times: %q", strings.Count(e, "sk-live-abc123"), e)
	}
}

func TestContactTokenEnvNeedsAURL(t *testing.T) {
	body := "[[sources]]\nid = \"eric\"\npath = \"/tmp/a.vcf\"\ntoken_env = \"CONTACTS_ERIC_TOKEN\"\n"
	if e := contactsError(t, body); !strings.Contains(e, "no url") {
		t.Errorf("error = %q", e)
	}
}

func TestContactSourceIDsAreUniqueAndWellFormed(t *testing.T) {
	dup := twoContactSources + "\n[[sources]]\nid = \"eric\"\npath = \"/tmp/b.vcf\"\n"
	if e := contactsError(t, dup); !strings.Contains(e, "duplicate source id") {
		t.Errorf("error = %q", e)
	}
	bad := "[[sources]]\nid = \"Eric's Phone\"\npath = \"/tmp/a.vcf\"\n"
	if e := contactsError(t, bad); !strings.Contains(e, "lowercase") {
		t.Errorf("error = %q", e)
	}
}

func TestContactKindIsAdmitOrBlock(t *testing.T) {
	body := "[[sources]]\nid = \"eric\"\npath = \"/tmp/a.vcf\"\nkind = \"deny\"\n"
	if e := contactsError(t, body); !strings.Contains(e, `must be "admit" or "block"`) {
		t.Errorf("error = %q", e)
	}
}

func TestEmptyContactsFileSaysToDeleteIt(t *testing.T) {
	// An empty inventory is not a state worth supporting: absence is the
	// rollback, and it is a complete one.
	if e := contactsError(t, "# nothing here\n"); !strings.Contains(e, "delete contacts.toml entirely") {
		t.Errorf("error = %q", e)
	}
}

// Unknown-key detection covers this file like the others, and a section written
// in the wrong file is named rather than reported as a typo — no edit distance
// can produce "that belongs in policy.toml".
func TestContactsRejectsUnknownKeysAndMisplacedSections(t *testing.T) {
	typo := "[[sources]]\nid = \"eric\"\npath = \"/tmp/a.vcf\"\npathh = \"/tmp/b.vcf\"\n"
	e := contactsError(t, typo)
	if !strings.Contains(e, `unknown key "pathh"`) || !strings.Contains(e, "did you mean path?") {
		t.Errorf("error = %q", e)
	}

	misplacedHere := twoContactSources + "\n[[people]]\nname = \"Grandma\"\nnumbers = [\"512-555-0100\"]\n"
	e = contactsError(t, misplacedHere)
	if !strings.Contains(e, "[[people]] belongs in policy.toml") {
		t.Errorf("error = %q", e)
	}
}

func TestMisplacedSourcesSectionIsNamedInPolicyAndHandsets(t *testing.T) {
	for _, file := range []string{"policy", "handsets"} {
		md, unknown := lintUnknown(t, file, "[[sources]]\nid = \"eric\"\n")
		_ = md
		var found bool
		for _, u := range unknown {
			if strings.Contains(u.String(), "[[sources]] belongs in contacts.toml") {
				found = true
			}
		}
		if !found {
			t.Errorf("%s.toml: [[sources]] was not named as belonging in contacts.toml: %v", file, unknown)
		}
	}
}

func TestLoadContactsIsSilentAboutAMissingFile(t *testing.T) {
	c, err := LoadContacts(filepath.Join(t.TempDir(), "contacts.toml"))
	if err != nil {
		t.Fatalf("LoadContacts: %v", err)
	}
	if c.Present() {
		t.Error("a missing contacts.toml compiled to a present inventory")
	}
	if c.Len() != 0 || len(c.All()) != 0 || len(c.IDs()) != 0 {
		t.Error("an absent inventory is not empty")
	}
	if c.Where() != "contacts.toml" {
		t.Errorf("Where() = %q", c.Where())
	}
}

func TestLoadContactsReadsAFileAndRemembersIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contacts.toml")
	if err := os.WriteFile(path, []byte(twoContactSources), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadContacts(path)
	if err != nil {
		t.Fatalf("LoadContacts: %v", err)
	}
	if !c.Present() {
		t.Error("a contacts.toml on disk compiled to an absent inventory")
	}
	if c.Where() != path {
		t.Errorf("Where() = %q, want %q", c.Where(), path)
	}
}

func TestLoadContactsPropagatesARealReadError(t *testing.T) {
	// A directory where a file should be: not "absent", and treating it as
	// absent would silently drop every address book the operator listed.
	if _, err := LoadContacts(t.TempDir()); err == nil {
		t.Error("an unreadable contacts.toml loaded as absent")
	}
}

func TestNilContactsInventoryIsInert(t *testing.T) {
	var c *Contacts
	if c.Present() || c.Len() != 0 || c.All() != nil || c.IDs() != nil {
		t.Error("nil *Contacts is not inert")
	}
	if c.Where() != "contacts.toml" {
		t.Errorf("Where() = %q", c.Where())
	}
}

func TestLintContactsReportsEveryProblemAtOnce(t *testing.T) {
	problems := LintContacts([]byte("[[sources]]\nid = \"Eric\"\n\n[[sources]]\nid = \"b\"\nkind = \"nope\"\n"))
	if len(problems) < 2 {
		t.Errorf("problems = %v, want the id and the kind reported together", problems)
	}
	if got := LintContacts([]byte(twoContactSources)); len(got) != 0 {
		t.Errorf("valid file reported problems: %v", got)
	}
	if got := LintContacts([]byte("[[sources]\n")); len(got) != 1 {
		t.Errorf("malformed TOML: %v", got)
	}
}

// lintUnknown decodes a document as one of the config files and returns the
// keys the schema had no field for.
func lintUnknown(t *testing.T, file, body string) (any, []UnknownKey) {
	t.Helper()
	switch file {
	case "policy", "handsets":
		_, unknown, err := decode([]byte(body), file)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		return nil, unknown
	default:
		t.Fatalf("unknown file %q", file)
		return nil, nil
	}
}
