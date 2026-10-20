package backup

import (
	"bytes"
	"encoding/base64"
	"html"
	"image/png"
	"regexp"
	"strings"
	"testing"
)

func TestRecoverySheetKeepsFactorOfflineAndDistinguishesSignIn(t *testing.T) {
	phrase, err := NewRecoveryPhrase()
	if err != nil {
		t.Fatal(err)
	}
	sheet, err := RecoverySheet(RecoveryScope{"synthetic-account", "synthetic-house", "synthetic-sheet"}, phrase)
	if err != nil {
		t.Fatal(err)
	}
	document := string(sheet)
	for _, text := range []string{
		"<h1>Backup decryption phrase</h1>",
		"Use in your terminal",
		"Account sign-in codes",
		"This phrase cannot sign you in",
		"Enter all eight groups together",
		`<p class="phrase">` + phrase + "</p>",
		"default-src 'none'", "connect-src 'none'", "form-action 'none'",
	} {
		if !strings.Contains(document, text) {
			t.Fatal("sheet lost offline or purpose guidance")
		}
	}
	if regexp.MustCompile(`(?i)<script|<link|<form|src="https?://|href="https?://`).Match(sheet) {
		t.Fatal("recovery sheet can send the factor to a network resource")
	}
	encoded := regexp.MustCompile(`src="data:image/png;base64,([^"]+)"`).FindStringSubmatch(document)
	if len(encoded) != 2 {
		t.Fatal("missing offline QR image")
	}
	pngBytes, err := base64.StdEncoding.DecodeString(html.UnescapeString(encoded[1]))
	if err != nil {
		t.Fatal("invalid inline QR")
	}
	if _, err := png.Decode(bytes.NewReader(pngBytes)); err != nil {
		t.Fatal("QR is not a PNG")
	}
	// Metadata is text, never trusted markup, even if a future caller uses labels.
	sheet, err = RecoverySheet(RecoveryScope{`<script>alert("synthetic")</script>`, "<img>", "<form>"}, phrase)
	if err != nil || bytes.Contains(sheet, []byte("<script>")) || bytes.Contains(sheet, []byte("<img>")) || bytes.Contains(sheet, []byte("<form>")) {
		t.Fatal("scope metadata became active markup")
	}
}
