package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"callmemaybe/internal/account"
	"callmemaybe/internal/backup"
	"filippo.io/age"
	"github.com/google/uuid"
	"golang.org/x/term"
)

type recoveryService interface {
	RecoveryStatus(context.Context) (account.RecoveryListing, error)
	RecoveryPrepare(context.Context, account.RecoveryGeneration, []byte) error
	RecoveryMaterial(context.Context, string) (account.RecoveryMaterial, error)
	RecoveryConfirm(context.Context, account.RecoveryGeneration) error
	RecoveryCancel(context.Context, string) error
	RecoveryDownload(context.Context, account.Bundle) ([]byte, error)
}
type recoveryOperation struct {
	Scope      backup.RecoveryScope       `json:"scope"`
	Generation account.RecoveryGeneration `json:"generation"`
	Envelope   []byte                     `json:"envelope"`
	Bundle     *account.Bundle            `json:"bundle,omitempty"`
	CipherHash string                     `json:"cipherHash,omitempty"`
	Root       string                     `json:"root,omitempty"`
}

var errRecoveryState = errors.New("recovery: cannot safely read or persist the pending operation")

func recoveryDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func recoveryPath(state, name string) string { return filepath.Join(state, "account", name) }
func saveRecoveryFile(state, name string, data []byte) error {
	dir := filepath.Join(state, "account")
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errRecoveryState
	}
	f, err := os.CreateTemp(dir, ".recovery-*")
	if err != nil {
		return errRecoveryState
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = recoveryFileOwner(f, info); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return errRecoveryState
	}
	if f.Sync() != nil || f.Close() != nil {
		return errRecoveryState
	}
	if os.Rename(f.Name(), filepath.Join(dir, name)) != nil {
		return errRecoveryState
	}
	d, err := os.Open(dir)
	if err != nil {
		return errRecoveryState
	}
	defer d.Close()
	if d.Sync() != nil {
		return errRecoveryState
	}
	return nil
}
func readRecoveryFile(path string, limit int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errRecoveryState
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > limit {
		return nil, errRecoveryState
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, errRecoveryState
	}
	return b, nil
}
func loadRecoveryOperation(state string) (*recoveryOperation, error) {
	path := recoveryPath(state, "recovery-operation.json")
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	b, err := readRecoveryFile(path, 128*1024)
	if err != nil {
		return nil, err
	}
	var op recoveryOperation
	if json.Unmarshal(b, &op) != nil || op.Scope.Generation != op.Generation.ID || recoveryDigest(op.Envelope) != op.Generation.SHA256 || len(op.Envelope) > 16384 {
		return nil, errRecoveryState
	}
	return &op, nil
}
func persistRecoveryOperation(state string, op *recoveryOperation) error {
	b, err := json.Marshal(op)
	if err != nil {
		return errRecoveryState
	}
	return saveRecoveryFile(state, "recovery-operation.json", b)
}
func operationScopeMatches(op *recoveryOperation, status account.RecoveryListing) bool {
	return op.Scope.Account == status.Account && op.Scope.House == status.House
}
func ensureRecoveryPrepared(ctx context.Context, c recoveryService, op *recoveryOperation) error {
	status, err := c.RecoveryStatus(ctx)
	if err != nil {
		return err
	}
	if !operationScopeMatches(op, status) {
		return errors.New("recovery: pending sheet belongs to a different account or house")
	}
	for _, g := range status.Generations {
		if g.ID == op.Generation.ID && g.State == "current" && g.SHA256 == op.Generation.SHA256 {
			return nil
		}
	}
	return c.RecoveryPrepare(ctx, op.Generation, op.Envelope)
}
func prepareRecoveryOperation(ctx context.Context, c recoveryService, state string, identity *age.X25519Identity, old *account.RecoveryGeneration, bundle *account.Bundle, cipher []byte, root string) (*recoveryOperation, error) {
	if existing, err := loadRecoveryOperation(state); err != nil {
		return nil, err
	} else if existing != nil {
		return existing, ensureRecoveryPrepared(ctx, c, existing)
	}
	status, err := c.RecoveryStatus(ctx)
	if err != nil {
		return nil, err
	}
	if old == nil && len(status.Generations) > 0 {
		return nil, errors.New("recovery: this house already has recovery material; use restore or resume the pending sheet")
	}
	phrase, err := backup.NewRecoveryPhrase()
	if err != nil {
		return nil, err
	}
	canonical, err := backup.NormaliseRecoveryPhrase(phrase)
	if err != nil {
		return nil, err
	}
	scope := backup.RecoveryScope{Account: status.Account, House: status.House, Generation: uuid.NewString()}
	envelope, err := backup.SealRecoveryIdentity(identity, scope, canonical)
	if err != nil {
		return nil, err
	}
	gen := account.RecoveryGeneration{ID: scope.Generation, Recipient: identity.Recipient().String(), SHA256: recoveryDigest(envelope), Bytes: len(envelope), State: "pending"}
	if old != nil {
		gen.Predecessor = &old.ID
	}
	op := &recoveryOperation{Scope: scope, Generation: gen, Envelope: envelope, Bundle: bundle, Root: root}
	sheet, err := backup.RecoverySheet(scope, phrase)
	if err != nil {
		return nil, err
	}
	// No server mutation until the sheet, ciphertext and operation have been fsynced.
	if err = saveRecoveryFile(state, "recovery-sheet-"+scope.Generation+".html", sheet); err != nil {
		return nil, err
	}
	if bundle != nil {
		op.CipherHash = recoveryDigest(cipher)
		if err = saveRecoveryFile(state, "recovery-bundle.age", cipher); err != nil {
			return nil, err
		}
	}
	if err = persistRecoveryOperation(state, op); err != nil {
		return nil, err
	}
	if err = ensureRecoveryPrepared(ctx, c, op); err != nil {
		return op, err
	}
	return op, nil
}
func recoveryPhraseFrom(file string) (string, error) {
	var data []byte
	if file != "" {
		var err error
		data, err = readRecoveryFile(file, 512)
		if err != nil {
			return "", err
		}
	} else {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return "", errors.New("recovery: use a terminal or a private --phrase-file; never put the phrase in command arguments")
		}
		fmt.Fprint(os.Stderr, "Backup decryption phrase — all eight groups (hidden): ")
		var err error
		data, err = term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", errors.New("recovery: phrase input cancelled")
		}
	}
	phrase, err := backup.NormaliseRecoveryPhrase(string(data))
	if err != nil {
		compact := strings.Join(strings.Fields(strings.ReplaceAll(string(data), "-", " ")), "")
		if len(compact) == 8 {
			return "", errors.New("recovery: this looks like an account sign-in code. Use that code at login.dialdoorman.cc/recover. Here, enter all eight groups from the separate Backup decryption phrase sheet")
		}
		return "", errors.New("recovery: enter all eight groups from the Backup decryption phrase sheet; account sign-in codes belong at login.dialdoorman.cc/recover")
	}
	return phrase, nil
}
func rootForRecovery(root string) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" {
		return errors.New("recovery: choose an absolute, new --root directory for inactive inspection")
	}
	if _, err := os.Lstat(root); err == nil {
		return errors.New("recovery: the inspection root already exists; choose a new directory")
	} else if !errors.Is(err, os.ErrNotExist) {
		return errRecoveryState
	}
	return nil
}
func prepareCloudRestore(ctx context.Context, c recoveryService, state, root, phrase string) (*recoveryOperation, error) {
	if op, err := loadRecoveryOperation(state); err != nil {
		return nil, err
	} else if op != nil {
		if op.Bundle == nil || op.Root != root {
			return nil, errors.New("recovery: resume with the same inspection root, or cancel the pending operation first")
		}
		return op, ensureRecoveryPrepared(ctx, c, op)
	}
	if err := rootForRecovery(root); err != nil {
		return nil, err
	}
	listing, err := c.RecoveryStatus(ctx)
	if err != nil {
		return nil, err
	}
	var current *account.RecoveryGeneration
	for _, g := range listing.Generations {
		if g.State == "current" {
			copy := g
			current = &copy
		}
	}
	if current == nil || len(listing.Bundles) == 0 {
		return nil, errors.New("recovery: no confirmed recovery sheet or stored backup exists for this house")
	}
	material, err := c.RecoveryMaterial(ctx, current.ID)
	if err != nil {
		return nil, err
	}
	scope := backup.RecoveryScope{Account: listing.Account, House: listing.House, Generation: current.ID}
	identity, err := backup.OpenRecoveryIdentity(material.Envelope, scope, phrase)
	if err != nil {
		return nil, err
	}
	if identity.Recipient().String() != current.Recipient {
		return nil, backup.ErrRecoveryScope
	}
	b := listing.Bundles[0]
	cipher, err := c.RecoveryDownload(ctx, b)
	if err != nil {
		return nil, err
	}
	if _, items, err := backup.OpenRecoveryBundle(cipher, identity); err != nil {
		return nil, err
	} else if err = validateRecoveryItems(items, state); err != nil {
		return nil, err
	}
	return prepareRecoveryOperation(ctx, c, state, identity, current, &b, cipher, root)
}
func validateRecoveryItems(items []backup.Item, state string) error {
	seen := map[string]bool{}
	for _, it := range items {
		p := filepath.ToSlash(it.Path)
		if seen[p] || filepath.Clean(p) != p || !filepath.IsAbs(p) || strings.Contains(p, "/../") || strings.Contains(p, "/account/") || strings.HasSuffix(p, "/backup/identity.key") || strings.HasPrefix(p, filepath.ToSlash(filepath.Join(state, "account"))+"/") {
			return errors.New("recovery: backup contains an unsafe path or device credentials")
		}
		seen[p] = true
	}
	return nil
}
func confirmRecoveryOperation(ctx context.Context, c recoveryService, state, phrase string) (int, error) {
	op, err := loadRecoveryOperation(state)
	if err != nil {
		return 0, err
	}
	if op == nil {
		return 0, errors.New("recovery: no sheet is waiting for confirmation")
	}
	if err = ensureRecoveryPrepared(ctx, c, op); err != nil {
		return 0, err
	}
	material, err := c.RecoveryMaterial(ctx, op.Generation.ID)
	if err != nil {
		return 0, err
	}
	if recoveryDigest(material.Envelope) != op.Generation.SHA256 {
		return 0, backup.ErrRecoveryEnvelope
	}
	key, err := backup.OpenRecoveryIdentity(material.Envelope, op.Scope, phrase)
	if err != nil {
		return 0, err
	}
	if key.Recipient().String() != op.Generation.Recipient {
		return 0, backup.ErrRecoveryScope
	}
	var items []backup.Item
	if op.Bundle != nil {
		cipher, e := readRecoveryFile(recoveryPath(state, "recovery-bundle.age"), 200000000)
		if e != nil {
			return 0, e
		}
		if recoveryDigest(cipher) != op.CipherHash {
			return 0, backup.ErrRecoveryEnvelope
		}
		_, items, err = backup.OpenRecoveryBundle(cipher, key)
		if err != nil {
			return 0, err
		}
		if err = validateRecoveryItems(items, state); err != nil {
			return 0, err
		}
	}
	// Only the ciphertext hash is sent. The phrase and its derivatives stay local.
	if err = c.RecoveryConfirm(ctx, op.Generation); err != nil {
		return 0, err
	}
	if op.Bundle != nil {
		if err = stageRecovery(items, op.Root); err != nil {
			return 0, err
		}
	}
	receipt := struct {
		Scope  backup.RecoveryScope `json:"scope"`
		Bundle *account.Bundle      `json:"bundle,omitempty"`
		Root   string               `json:"root,omitempty"`
		Files  int                  `json:"files"`
	}{op.Scope, op.Bundle, op.Root, len(items)}
	data, _ := json.Marshal(receipt)
	if err = saveRecoveryFile(state, "recovery-receipt.json", data); err != nil {
		return 0, err
	}
	if err = os.Remove(recoveryPath(state, "recovery-operation.json")); err != nil {
		return 0, errRecoveryState
	}
	// Keep the printable sheet until the customer chooses where to retain it. It is
	// private and excluded from backups, never copied into the restored house.
	_ = os.Remove(recoveryPath(state, "recovery-bundle.age"))
	return len(items), nil
}
func stageRecovery(items []backup.Item, root string) error {
	// A retry after the directory rename verifies every byte and mode; it never overwrites.
	if info, err := os.Lstat(root); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errRecoveryState
		}
		return verifyRecoveryFiles(items, root)
	}
	if err := rootForRecovery(root); err != nil {
		return err
	}
	parent := filepath.Dir(root)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return errRecoveryState
	}
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || resolved != parent {
		return errRecoveryState
	}
	info, err := os.Stat(parent)
	if err != nil || info.Mode().Perm()&0022 != 0 {
		return errRecoveryState
	}
	temporary, err := os.MkdirTemp(parent, ".doorman-restore-")
	if err != nil {
		return errRecoveryState
	}
	defer os.RemoveAll(temporary)
	_, unowned, err := backup.Place(items, temporary)
	if err != nil {
		return errors.New("recovery: writing inspection files failed; the source and target services were not changed")
	}
	if len(unowned) > 0 {
		return errors.New("recovery: file ownership could not be restored; create the required users before retrying as root")
	}
	if err = verifyRecoveryFiles(items, temporary); err != nil {
		return err
	}
	if err = syncRecoveryTree(temporary); err != nil {
		return err
	}
	if err = os.Rename(temporary, root); err != nil {
		return errRecoveryState
	}
	return syncRecoveryPath(parent)
}
func verifyRecoveryFiles(items []backup.Item, root string) error {
	expected := map[string]bool{}
	for _, it := range items {
		path := filepath.Join(root, it.Path)
		expected[path] = true
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || resolved != path {
			return errRecoveryState
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return errRecoveryState
		}

		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			if it.Owner != "" {
				u, e := user.Lookup(it.Owner)
				if e != nil || u.Uid != strconv.FormatUint(uint64(st.Uid), 10) {
					return errors.New("recovery: restored ownership differs; retry with permission to restore owners")
				}
			}
			if it.Group != "" {
				g, e := user.LookupGroup(it.Group)
				if e != nil || g.Gid != strconv.FormatUint(uint64(st.Gid), 10) {
					return errors.New("recovery: restored group differs; retry with permission to restore owners")
				}
			}
		}
		data, err := os.ReadFile(path)
		mode := os.FileMode(it.Mode)
		if mode == 0 {
			mode = 0600
		}
		if err != nil || !bytes.Equal(data, it.Content) || info.Mode().Perm() != mode.Perm() {
			return errors.New("recovery: restored file verification failed")
		}
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return errRecoveryState
		}
		if !entry.IsDir() && !expected[path] {
			return errRecoveryState
		}
		return nil
	})
}
func runRecovery(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: doorman recovery authorize|setup|restore|confirm|cancel|status [--state DIR] [--root DIR] [--phrase-file FILE]")
		return 2
	}
	fs := flag.NewFlagSet("recovery "+args[0], flag.ContinueOnError)
	state := fs.String("state", "/var/lib/doorman", "private device state directory")
	root := fs.String("root", "", "new inactive inspection directory (required for restore)")
	phraseFile := fs.String("phrase-file", "", "private file containing the printed phrase; otherwise hidden terminal input")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 {
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	client := account.New(*state)
	if args[0] == "authorize" {
		scope, err := client.AuthorizeRecovery(ctx, func(code string) {
			fmt.Println("Open", account.RecoveryApprovalURL)
			fmt.Println("Approve recovery with code", code)
			fmt.Println("Waiting for your passkey approval…")
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("Recovery approved for house %s until %s.\n", scope.House, time.UnixMilli(scope.ExpiresAt).Format(time.RFC3339))
		return 0
	}
	// The separate lock serializes sheet changes, without blocking ordinary backup uploads.
	lock, err := os.OpenFile(recoveryPath(*state, ".recovery.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		fmt.Fprintln(os.Stderr, errRecoveryState)
		return 1
	}
	defer lock.Close()
	info, statErr := os.Lstat(filepath.Join(*state, "account"))
	if statErr != nil || recoveryFileOwner(lock, info) != nil {
		fmt.Fprintln(os.Stderr, errRecoveryState)
		return 1
	}
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		fmt.Fprintln(os.Stderr, "recovery: another recovery command is running")
		return 1
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	var op *recoveryOperation
	switch args[0] {
	case "status":
		listing, e := client.RecoveryStatus(ctx)
		err = e
		if err == nil {
			fmt.Printf("Recovery: %d generation(s), %d stored backup(s), house %s.\n", len(listing.Generations), len(listing.Bundles), listing.House)
			for _, g := range listing.Generations {
				fmt.Printf("  %s: %s, %d verified encrypted copies.\n", g.ID, g.State, g.HealthyCopies)
			}
		}
	case "setup":
		existing, e := loadRecoveryOperation(*state)
		if e != nil {
			err = e
			break
		}
		if existing != nil {
			op = existing
			err = ensureRecoveryPrepared(ctx, client, op)
			break
		}
		raw, ok := readIdentity(*state)
		if !ok {
			return 1
		}
		key, e := age.ParseX25519Identity(raw)
		if e != nil {
			err = backup.ErrRecoveryEnvelope
			break
		}
		op, err = prepareRecoveryOperation(ctx, client, *state, key, nil, nil, nil, "")
	case "restore":
		existing, e := loadRecoveryOperation(*state)
		if e != nil {
			err = e
			break
		}
		if existing != nil {
			if existing.Bundle == nil || existing.Root != *root {
				err = errors.New("recovery: resume with the same inspection root, or cancel the pending operation first")
				break
			}
			op = existing
			err = ensureRecoveryPrepared(ctx, client, op)
			break
		}
		if err = rootForRecovery(*root); err != nil {
			break
		}
		phrase, e := recoveryPhraseFrom(*phraseFile)
		if e != nil {
			err = e
			break
		}
		op, err = prepareCloudRestore(ctx, client, *state, *root, phrase)
	case "cancel":
		err = cancelRecoveryOperation(ctx, client, *state)
		if err == nil {
			fmt.Println("Unconfirmed recovery sheet cancelled. The current phrase is unchanged.")
		}
	case "confirm":
		phrase, e := recoveryPhraseFrom(*phraseFile)
		if e != nil {
			err = e
			break
		}
		var n int
		n, err = confirmRecoveryOperation(ctx, client, *state, phrase)
		if err == nil {
			if n > 0 {
				fmt.Printf("Verified %d restored files in the inactive inspection root. No services started; source unchanged.\n", n)
			} else {
				fmt.Println("Printed recovery sheet confirmed in two encrypted copies.")
			}
		}
	default:
		fmt.Fprintln(os.Stderr, "recovery: unknown command")
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, recoveryErrorMessage(err))
		return 1
	}
	if op != nil {
		if op.Root != "" {
			fmt.Println("Inspection root:", op.Root)
		}
		fmt.Println("Print or safely save:", recoveryPath(*state, "recovery-sheet-"+op.Scope.Generation+".html"))
		fmt.Println("Then run doorman recovery confirm and enter the NEW sheet's phrase privately.")
		fmt.Println("The previous phrase remains active until confirmation.")
	}
	return 0
}

func recoveryErrorMessage(err error) string {
	if errors.Is(err, backup.ErrRecoveryEnvelope) {
		return "recovery: the backup recovery material could not be opened. Check all eight groups from the matching Backup decryption phrase sheet; an account sign-in code cannot unlock a backup"
	}
	return err.Error()
}

func recoveryFileOwner(f *os.File, directory os.FileInfo) error {
	if os.Geteuid() != 0 {
		return nil
	}
	stat, ok := directory.Sys().(*syscall.Stat_t)
	if !ok || f.Chown(int(stat.Uid), int(stat.Gid)) != nil {
		return errRecoveryState
	}
	return nil
}
func syncRecoveryPath(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return errRecoveryState
	}
	defer f.Close()
	if f.Sync() != nil {
		return errRecoveryState
	}
	return nil
}

// Files and nested directories are durable before publishing the staged root.
func syncRecoveryTree(root string) error {
	var directories []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return errRecoveryState
		}
		if entry.IsDir() {
			directories = append(directories, path)
			return nil
		}
		return syncRecoveryPath(path)
	})
	if err != nil {
		return err
	}
	for i := len(directories) - 1; i >= 0; i-- {
		if err := syncRecoveryPath(directories[i]); err != nil {
			return err
		}
	}
	return nil
}

func cancelRecoveryOperation(ctx context.Context, c recoveryService, state string) error {
	op, err := loadRecoveryOperation(state)
	if err != nil {
		return err
	}
	if op == nil {
		return errors.New("recovery: no pending operation to cancel")
	}
	// A lost confirmation response must not let cancellation abandon a current sheet.
	if err = c.RecoveryCancel(ctx, op.Generation.ID); err != nil {
		return err
	}
	if err = os.Remove(recoveryPath(state, "recovery-operation.json")); err != nil {
		return errRecoveryState
	}
	_ = os.Remove(recoveryPath(state, "recovery-bundle.age"))
	return syncRecoveryPath(filepath.Join(state, "account"))
}
