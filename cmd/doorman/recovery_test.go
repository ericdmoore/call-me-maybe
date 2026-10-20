package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"callmemaybe/internal/account"
	"callmemaybe/internal/backup"
	"filippo.io/age"
)

type fakeRecoveryService struct {
	listing                  account.RecoveryListing
	materials                map[string]account.RecoveryMaterial
	cipher                   []byte
	failPrepare, loseConfirm bool
	downloads                int
}

func newRecoveryService() *fakeRecoveryService {
	return &fakeRecoveryService{listing: account.RecoveryListing{Account: "00000000-0000-7000-8000-000000000001", House: "00000000-0000-7000-8000-000000000002", Box: "00000000-0000-7000-8000-000000000003"}, materials: map[string]account.RecoveryMaterial{}}
}
func (f *fakeRecoveryService) RecoveryStatus(context.Context) (account.RecoveryListing, error) {
	return f.listing, nil
}
func (f *fakeRecoveryService) RecoveryPrepare(_ context.Context, g account.RecoveryGeneration, b []byte) error {
	if f.failPrepare {
		return errors.New("synthetic missing copy")
	}
	for _, current := range f.listing.Generations {
		if current.ID == g.ID {
			return nil
		}
	}
	g.State = "pending"
	g.HealthyCopies = 2
	f.listing.Generations = append(f.listing.Generations, g)
	f.materials[g.ID] = account.RecoveryMaterial{Account: f.listing.Account, House: f.listing.House, Generation: g, Envelope: b}
	return nil
}
func (f *fakeRecoveryService) RecoveryMaterial(_ context.Context, id string) (account.RecoveryMaterial, error) {
	m, ok := f.materials[id]
	if !ok {
		return m, errors.New("synthetic absent material")
	}
	return m, nil
}
func (f *fakeRecoveryService) RecoveryConfirm(_ context.Context, g account.RecoveryGeneration) error {
	old := f.listing.Generations
	f.listing.Generations = nil
	for _, x := range old {
		if x.ID == g.ID {
			x.State = "current"
			f.listing.Generations = append(f.listing.Generations, x)
		}
	}
	if len(f.listing.Generations) != 1 {
		return errors.New("synthetic generation conflict")
	}
	if f.loseConfirm {
		f.loseConfirm = false
		return errors.New("synthetic lost confirmation response")
	}
	return nil
}
func (f *fakeRecoveryService) RecoveryDownload(context.Context, account.Bundle) ([]byte, error) {
	f.downloads++
	return f.cipher, nil
}
func recoveryTestState(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(dir, "account"), 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}
func sheetPhrase(t *testing.T, state string, op *recoveryOperation) string {
	t.Helper()
	data, err := os.ReadFile(recoveryPath(state, "recovery-sheet-"+op.Scope.Generation+".html"))
	if err != nil {
		t.Fatal(err)
	}
	typed := regexp.MustCompile(`[A-HJ-NP-Z2-9]{4}(?:-[A-HJ-NP-Z2-9]{4}){7}`).FindString(string(data))
	p, err := backup.NormaliseRecoveryPhrase(typed)
	if err != nil {
		t.Fatal("synthetic sheet lacked phrase")
	}
	return p
}
func syntheticRecoveryBundle(t *testing.T, key *age.X25519Identity) ([]byte, []backup.Item) {
	t.Helper()
	items := []backup.Item{{File: backup.File{Path: "/opt/synthetic/policy.toml", Mode: 0600, Tier: "house"}, Content: []byte("[house]\nlabel='synthetic'\n")}, {File: backup.File{Path: "/var/lib/doorman/synthetic.txt", Mode: 0640, Tier: "state"}, Content: []byte("expected recovery content")}}
	m := &backup.Manifest{Format: backup.FormatVersion, Doorman: "synthetic", Host: "synthetic-source", CreatedAt: time.Now()}
	for i := range items {
		items[i].Size = int64(len(items[i].Content))
		items[i].SHA256 = recoveryDigest(items[i].Content)
		m.Files = append(m.Files, items[i].File)
	}
	var b bytes.Buffer
	if err := backup.Bundle(&b, m, items, key.Recipient().String()); err != nil {
		t.Fatal(err)
	}
	return b.Bytes(), items
}
func TestPrintedRecoveryFreshDeviceRehearsal(t *testing.T) {
	ctx := context.Background()
	svc := newRecoveryService()
	source := recoveryTestState(t)
	target := recoveryTestState(t)
	key, _ := age.GenerateX25519Identity()
	svc.cipher, _ = syntheticRecoveryBundle(t, key)
	op, err := prepareRecoveryOperation(ctx, svc, source, key, nil, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	firstPhrase := sheetPhrase(t, source, op)
	if n, err := confirmRecoveryOperation(ctx, svc, source, firstPhrase); err != nil || n != 0 {
		t.Fatalf("setup: %v", err)
	}
	svc.listing.Bundles = []account.Bundle{{ID: "00000000-0000-7000-8000-000000000004", Bytes: int64(len(svc.cipher))}}
	root := filepath.Join(target, "inspection", "root")
	// The replacement has independent management state, which must survive untouched.
	tokenPath := recoveryPath(target, "tokens.bin")
	os.WriteFile(tokenPath, []byte("synthetic-target-credentials"), 0600)
	next, err := prepareCloudRestore(ctx, svc, target, root, firstPhrase)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("restore wrote files before sheet confirmation")
	}
	if len(svc.listing.Generations) != 2 {
		t.Fatal("preparation retired the working phrase")
	}
	nextPhrase := sheetPhrase(t, target, next)
	if firstPhrase == nextPhrase {
		t.Fatal("phrase reused")
	}
	// Simulate confirmation committing while its HTTP response is lost.
	svc.loseConfirm = true
	if _, err = confirmRecoveryOperation(ctx, svc, target, nextPhrase); err == nil {
		t.Fatal("lost response did not stop")
	}
	if _, err = os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("wrote after ambiguous confirmation")
	}
	n, err := confirmRecoveryOperation(ctx, svc, target, nextPhrase)
	if err != nil || n != 2 {
		t.Fatalf("resumed restore: n=%d err=%v", n, err)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "opt/synthetic/policy.toml")); string(got) != "[house]\nlabel='synthetic'\n" {
		t.Fatal("wrong restored file")
	}
	if info, _ := os.Stat(filepath.Join(root, "var/lib/doorman/synthetic.txt")); info.Mode().Perm() != 0640 {
		t.Fatal("mode not restored")
	}
	if got, _ := os.ReadFile(tokenPath); string(got) != "synthetic-target-credentials" {
		t.Fatal("destination credentials changed")
	}
	material := svc.materials[next.Generation.ID]
	if _, err = backup.OpenRecoveryIdentity(material.Envelope, next.Scope, firstPhrase); err == nil {
		t.Fatal("spent phrase opens replacement")
	}
	if _, err = loadRecoveryOperation(target); err != nil {
		t.Fatal(err)
	}
	if pending, _ := loadRecoveryOperation(target); pending != nil {
		t.Fatal("completed operation still pending")
	}
}
func TestRecoveryInterruptionWrongFactorAndWrongAccount(t *testing.T) {
	ctx := context.Background()
	svc := newRecoveryService()
	state := recoveryTestState(t)
	key, _ := age.GenerateX25519Identity()
	svc.failPrepare = true
	op, err := prepareRecoveryOperation(ctx, svc, state, key, nil, nil, nil, "")
	if err == nil || op == nil {
		t.Fatal("did not preserve interrupted setup")
	}
	phrase := sheetPhrase(t, state, op)
	svc.failPrepare = false
	resumed, err := prepareRecoveryOperation(ctx, svc, state, key, nil, nil, nil, "")
	if err != nil || resumed.Generation.ID != op.Generation.ID {
		t.Fatal("replaced interrupted sheet")
	}
	wrong, _ := backup.NewRecoveryPhrase()
	wrong, _ = backup.NormaliseRecoveryPhrase(wrong)
	if _, err = confirmRecoveryOperation(ctx, svc, state, wrong); err == nil {
		t.Fatal("wrong phrase confirmed")
	}
	if svc.listing.Generations[0].State != "pending" {
		t.Fatal("wrong phrase retired working material")
	}
	savedAccount := svc.listing.Account
	svc.listing.Account = "another-account"
	if _, err = confirmRecoveryOperation(ctx, svc, state, phrase); err == nil {
		t.Fatal("wrong account confirmed")
	}
	svc.listing.Account = savedAccount
	if _, err = confirmRecoveryOperation(ctx, svc, state, phrase); err != nil {
		t.Fatal(err)
	}
	svc.cipher, _ = syntheticRecoveryBundle(t, key)
	svc.listing.Bundles = []account.Bundle{{ID: "synthetic", Bytes: int64(len(svc.cipher))}}
	target := recoveryTestState(t)
	if _, err = prepareCloudRestore(ctx, svc, target, filepath.Join(target, "root"), wrong); err == nil {
		t.Fatal("wrong factor recovered")
	}
	if svc.downloads != 0 {
		t.Fatal("downloaded before proving factor")
	}
}
func TestRecoveryRejectsTamperAndUnsafeStaging(t *testing.T) {
	key, _ := age.GenerateX25519Identity()
	cipher, items := syntheticRecoveryBundle(t, key)
	for _, data := range [][]byte{cipher[:len(cipher)-1], append(append([]byte{}, cipher...), 0)} {
		if _, _, err := backup.OpenRecoveryBundle(data, key); err == nil {
			t.Fatal("accepted damaged full stream")
		}
	}
	bad := append([]byte{}, cipher...)
	bad[len(bad)-1] ^= 1
	if _, _, err := backup.OpenRecoveryBundle(bad, key); err == nil {
		t.Fatal("accepted final tag tampering")
	}
	if _, _, err := backup.OpenRecoveryBundle(cipher, nil); err == nil {
		t.Fatal("nil key")
	}
	for _, path := range []string{"relative", "/opt/../etc/shadow", "/var/lib/doorman/account/tokens.bin", "/var/lib/doorman/backup/identity.key"} {
		if validateRecoveryItems([]backup.Item{{File: backup.File{Path: path}}}, "/var/lib/doorman") == nil {
			t.Fatal("unsafe path accepted")
		}
	}
	if validateRecoveryItems([]backup.Item{items[0], items[0]}, "/var/lib/doorman") == nil {
		t.Fatal("duplicate path accepted")
	}
	state := recoveryTestState(t)
	root := filepath.Join(state, "inspection")
	if err := stageRecovery(items, root); err != nil {
		t.Fatal(err)
	}
	if err := stageRecovery(items, root); err != nil {
		t.Fatal("could not resume after rename", err)
	}
	os.WriteFile(filepath.Join(root, "unexpected"), []byte("extra"), 0600)
	if stageRecovery(items, root) == nil {
		t.Fatal("accepted unexpected existing file")
	}
	for _, r := range []string{"", "/", "relative", filepath.Join(state, "account")} {
		if rootForRecovery(r) == nil {
			t.Fatal("unsafe root accepted")
		}
	}
	outside := recoveryTestState(t)
	link := filepath.Join(state, "linked")
	os.Symlink(outside, link)
	if stageRecovery(items, filepath.Join(link, "root")) == nil {
		t.Fatal("followed staging symlink")
	}
}
func TestRecoveryPhraseFilesAndPrivatePersistence(t *testing.T) {
	state := recoveryTestState(t)
	phrase, _ := backup.NewRecoveryPhrase()
	path := filepath.Join(state, "phrase")
	os.WriteFile(path, []byte(phrase), 0600)
	if _, err := recoveryPhraseFrom(path); err != nil {
		t.Fatal(err)
	}
	os.Chmod(path, 0644)
	if _, err := recoveryPhraseFrom(path); err == nil {
		t.Fatal("public phrase file")
	}
	os.Chmod(path, 0600)
	link := filepath.Join(state, "link")
	os.Symlink(path, link)
	if _, err := recoveryPhraseFrom(link); err == nil {
		t.Fatal("phrase symlink")
	}
	if _, err := backup.NormaliseRecoveryPhrase("human-chosen short password"); err == nil {
		t.Fatal("weak phrase accepted")
	}
	if _, err := backup.RecoverySheet(backup.RecoveryScope{}, phrase); err == nil {
		t.Fatal("missing scope")
	}
	if rc := runRecovery(nil); rc != 2 {
		t.Fatal(rc)
	}
	if rc := runRecovery([]string{"unknown", "--state", state}); rc != 2 {
		t.Fatal(rc)
	}
	if rc := runRecovery([]string{"restore", "--state", state}); rc == 0 {
		t.Fatal("restore without root")
	}
}

func TestRecoveryPhraseInputExplainsAccountCodesWithoutEchoingSecrets(t *testing.T) {
	path := filepath.Join(recoveryTestState(t), "phrase")
	for _, input := range []string{"ABCD-EFGH", "abcd efgh\n", "ABCDEFGH", "short", strings.Repeat("0", 32)} {
		if err := os.WriteFile(path, []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		phrase, err := recoveryPhraseFrom(path)
		if err == nil || phrase != "" {
			t.Fatal("accepted a sign-in code or malformed backup phrase")
		}
		message := err.Error()
		if !strings.Contains(message, "eight groups") || !strings.Contains(message, "login.dialdoorman.cc/recover") {
			t.Fatal("missing actionable distinction between the sheets")
		}
		if strings.Contains(message, strings.TrimSpace(input)) {
			t.Fatal("input echoed in error")
		}
	}
	valid, err := backup.NewRecoveryPhrase()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.ToLower(strings.ReplaceAll(valid, "-", " "))+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := recoveryPhraseFrom(path)
	want, _ := backup.NormaliseRecoveryPhrase(valid)
	if err != nil || got != want {
		t.Fatal("valid eight-group phrase no longer accepted")
	}
}

func TestRecoveryEnvelopeFailureGuidanceDoesNotExposeCiphertextOrPhrase(t *testing.T) {
	message := recoveryErrorMessage(backup.ErrRecoveryEnvelope)
	if !strings.Contains(message, "matching Backup decryption phrase sheet") || !strings.Contains(message, "account sign-in code cannot unlock a backup") {
		t.Fatal("wrong-factor failure lacks sheet guidance")
	}
	network := errors.New("account: service connection failed; credentials were not logged")
	if recoveryErrorMessage(network) != network.Error() {
		t.Fatal("unrelated failure presented as a wrong phrase")
	}
}

func (f *fakeRecoveryService) RecoveryCancel(_ context.Context, id string) error {
	for i, g := range f.listing.Generations {
		if g.ID != id {
			continue
		}
		if g.State != "pending" {
			return errors.New("synthetic current sheet cannot be cancelled")
		}
		f.listing.Generations = append(f.listing.Generations[:i], f.listing.Generations[i+1:]...)
		return nil
	}
	return nil
}
func TestRecoveryCancelKeepsCurrentSheetAndRequiresUnconfirmedOperation(t *testing.T) {
	f := newRecoveryService()
	state := recoveryTestState(t)
	key, _ := age.GenerateX25519Identity()
	op, err := prepareRecoveryOperation(context.Background(), f, state, key, nil, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	f.listing.Generations[0].State = "current"
	if cancelRecoveryOperation(context.Background(), f, state) == nil {
		t.Fatal("abandoned current phrase")
	}
	if got, _ := loadRecoveryOperation(state); got == nil {
		t.Fatal("lost current operation")
	}
	f.listing.Generations[0].State = "pending"
	if err = cancelRecoveryOperation(context.Background(), f, state); err != nil {
		t.Fatal(err)
	}
	if got, _ := loadRecoveryOperation(state); got != nil {
		t.Fatal("cancelled operation remains")
	}
	if _, err = os.Stat(recoveryPath(state, "recovery-sheet-"+op.Scope.Generation+".html")); err != nil {
		t.Fatal("cancel silently deleted customer's printout")
	}
}
