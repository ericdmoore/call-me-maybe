package backup

import (
	"bytes"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"html/template"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

const phraseAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// NewRecoveryPhrase draws 160 random bits. The eight four-character groups avoid
// ambiguous I/O/0/1 and are printed verbatim; human-chosen passwords are not used.
func NewRecoveryPhrase() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", ErrRecoveryEnvelope
	}
	for i := range raw {
		raw[i] = phraseAlphabet[raw[i]&31]
	}
	return FormatRecoveryPhrase(string(raw)), nil
}
func NormaliseRecoveryPhrase(phrase string) (string, error) {
	p := strings.ToUpper(strings.Join(strings.Fields(strings.ReplaceAll(phrase, "-", " ")), ""))
	if len(p) != 32 {
		return "", ErrRecoveryEnvelope
	}
	for _, c := range p {
		if !strings.ContainsRune(phraseAlphabet, c) {
			return "", ErrRecoveryEnvelope
		}
	}
	return p, nil
}
func FormatRecoveryPhrase(p string) string {
	var groups []string
	for len(p) > 4 {
		groups = append(groups, p[:4])
		p = p[4:]
	}
	groups = append(groups, p)
	return strings.Join(groups, "-")
}

// RecoverySheet is standalone HTML: no network resources, scripts, analytics or
// service credentials. The QR holds the phrase only after #, never in a request.
func RecoverySheet(scope RecoveryScope, phrase string) ([]byte, error) {
	p, err := NormaliseRecoveryPhrase(phrase)
	if err != nil || !validRecoveryScope(scope) {
		return nil, ErrRecoveryEnvelope
	}
	link := "https://login.dialdoorman.cc/backup-key#phrase=" + p
	png, err := qrcode.Encode(link, qrcode.Medium, 320)
	if err != nil {
		return nil, ErrRecoveryEnvelope
	}
	data := struct {
		Scope  RecoveryScope
		Phrase string
		QR     template.URL
	}{scope, FormatRecoveryPhrase(p), template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(png))}
	var out bytes.Buffer
	err = sheetTemplate.Execute(&out, data)
	return out.Bytes(), err
}

//go:embed recovery_sheet.html
var recoverySheetHTML string

var sheetTemplate = template.Must(template.New("sheet").Parse(recoverySheetHTML))
