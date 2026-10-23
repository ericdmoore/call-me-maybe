package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// The last good copy is what makes "an invalid file never replaces the last
// good state" hold across a restart, not only across a reload. Every time a
// line's policy is accepted — at startup or on reload — the exact bytes that
// were compiled are copied beside it:
//
//	policy.last-good/<line> -> .<line>-<digest>/{policy.toml, handsets.toml, trunks.toml}
//
// The name is a symlink swapped by rename onto a content-addressed directory
// that was complete before the swap, so a crash leaves either the previous
// copy or the new one, never half of either. A copy is only ever written from
// files the shared validator accepted.
//
// At startup the daemon falls back to this copy when the current files are
// refused, which is how a release that tightens validation cannot leave a box
// that was answering calls yesterday with no lobby today.

// Names of the files inside a last good copy. The policy file is stored as
// policy.toml whichever line it belongs to.
const (
	lastGoodPolicy   = "policy.toml"
	lastGoodHandsets = "handsets.toml"
	lastGoodTrunks   = "trunks.toml"
)

// LastGoodPath is where the last good copy of a line's configuration lives:
// policy.last-good/<line> beside policy.toml. A directory, so line discovery
// (which looks for policy.<line>.toml files) never mistakes it for a line.
func LastGoodPath(policyPath, line string) string {
	stem := strings.TrimSuffix(filepath.Base(policyPath), ".toml")
	return filepath.Join(filepath.Dir(policyPath), stem+".last-good", line)
}

type lastGoodFile struct {
	name string // inside the copy
	from string // the original, whose owner the copy takes
	data []byte
}

// writeLastGood atomically points link at a directory holding files.
func writeLastGood(link string, files []lastGoodFile) error {
	parent, base := filepath.Dir(link), filepath.Base(link)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	h := sha256.New()
	for _, f := range files {
		fmt.Fprintf(h, "%s\x00%d\x00", f.name, len(f.data))
		h.Write(f.data)
	}
	version := "." + base + "-" + hex.EncodeToString(h.Sum(nil))[:16]
	final := filepath.Join(parent, version)
	if target, err := os.Readlink(link); err == nil && target == version {
		return nil // already the last good copy
	}
	if _, err := os.Stat(final); err != nil {
		tmp, err := os.MkdirTemp(parent, "."+base+"-tmp-")
		if err != nil {
			return err
		}
		if err := fillLastGood(tmp, files); err != nil {
			_ = os.RemoveAll(tmp)
			return err
		}
		if err := os.Rename(tmp, final); err != nil {
			_ = os.RemoveAll(tmp)
			if _, statErr := os.Stat(final); statErr != nil {
				return err
			}
		}
	}
	tmpLink := filepath.Join(parent, "."+base+"-link-"+hex.EncodeToString(h.Sum(nil))[16:24])
	_ = os.Remove(tmpLink)
	if err := os.Symlink(version, tmpLink); err != nil {
		return err
	}
	if err := os.Rename(tmpLink, link); err != nil {
		_ = os.Remove(tmpLink)
		return err
	}
	syncDir(parent)
	pruneLastGood(parent, base, version)
	return nil
}

func fillLastGood(dir string, files []lastGoodFile) error {
	for _, f := range files {
		path := filepath.Join(dir, f.name)
		out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, err = out.Write(f.data)
		if err == nil {
			err = out.Sync()
		}
		if closeErr := out.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		copyOwner(path, f.from)
	}
	syncDir(dir)
	return nil
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

// pruneLastGood removes this line's superseded copies and any temporary left
// by a crash. Only this line's names match: another line's copies are another
// store's business.
func pruneLastGood(parent, base, keep string) {
	ours := regexp.MustCompile(`^\.` + regexp.QuoteMeta(base) + `-([0-9a-f]{16}|tmp-[0-9]+)$`)
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.Name() != keep && ours.MatchString(e.Name()) {
			_ = os.RemoveAll(filepath.Join(parent, e.Name()))
		}
	}
}

// saveLastGood copies the files p was compiled from, if they still hold the
// bytes it was compiled from. A file edited since will be saved by the reload
// that edit triggers, if it is accepted.
func (s *Store) saveLastGood(p *Policy) error {
	if s.opts.LastGood == "" {
		return nil
	}
	pdata, err := os.ReadFile(s.policyPath)
	if err != nil {
		return err
	}
	files := []lastGoodFile{{lastGoodPolicy, s.policyPath, pdata}}
	var hdata []byte
	if s.handsetsPath != "" {
		hdata, err = os.ReadFile(s.handsetsPath)
		switch {
		case err == nil:
			files = append(files, lastGoodFile{lastGoodHandsets, s.handsetsPath, hdata})
		case errors.Is(err, os.ErrNotExist):
			hdata = nil
		default:
			return err
		}
	}
	if SourceDigest(pdata, hdata) != p.SourceDigest() {
		return nil
	}
	if s.opts.TrunksPath != "" {
		tdata, err := os.ReadFile(s.opts.TrunksPath)
		switch {
		case err == nil:
			files = append(files, lastGoodFile{lastGoodTrunks, s.opts.TrunksPath, tdata})
		case !errors.Is(err, os.ErrNotExist):
			return err
		}
	}
	return writeLastGood(s.opts.LastGood, files)
}

// loadLastGood compiles the last good copy with the same rules as the
// current files. A copy is self-contained: its trunk references are checked
// against the trunks.toml saved with it.
func loadLastGood(dir string, split, trunks bool, o Options) (*Policy, error) {
	handsets := ""
	if split {
		handsets = filepath.Join(dir, lastGoodHandsets)
	}
	if trunks {
		o.TrunksPath = filepath.Join(dir, lastGoodTrunks)
	}
	o.OnUnknownKey = nil
	return LoadSplitWith(filepath.Join(dir, lastGoodPolicy), handsets, o)
}
