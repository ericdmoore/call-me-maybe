package backup

import (
	"bytes"
	"crypto/rand"
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

var sheetTemplate = template.Must(template.New("sheet").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="referrer" content="no-referrer"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src data:; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'"><title>Doorman backup recovery sheet</title><style>body{font:18px/1.5 system-ui,sans-serif;max-width:760px;margin:36px auto;padding:24px;color:#17241e}h1{font-size:32px}.phrase{font:22px/1.7 monospace;overflow-wrap:anywhere;border:2px solid;padding:18px}img{width:240px;height:240px}dl{font-size:12px;overflow-wrap:anywhere}dt{font-weight:bold}dd{margin:0 0 8px}small{font-size:13px}@media print{body{margin:0;padding:12mm}img{break-inside:avoid}}</style><h1>Your backup recovery sheet</h1><p>Keep a printed or safely saved copy away from your Brain. Anyone with this phrase and your encrypted recovery material can unlock your backups.</p><p class="phrase">{{.Phrase}}</p><img src="{{.QR}}" alt="QR containing the same recovery phrase in the link fragment"><p>To recover: sign in on the replacement device, run <code>doorman recovery authorize</code>, then <code>doorman recovery restore --root /your/inspection/folder</code>. Enter this phrase privately when asked.</p><p>After using this phrase, save and confirm the replacement sheet. The previous phrase remains active until confirmation. Rotating the hosted copy cannot erase copies already downloaded elsewhere.</p><dl><dt>Account</dt><dd>{{.Scope.Account}}</dd><dt>House</dt><dd>{{.Scope.House}}</dd><dt>Generation</dt><dd>{{.Scope.Generation}}</dd></dl><small>This is your backup-unlock phrase. Keep your account sign-in recovery codes with this sheet; those codes are managed separately. A password or account recovery alone cannot decrypt a backup.</small></html>`))
