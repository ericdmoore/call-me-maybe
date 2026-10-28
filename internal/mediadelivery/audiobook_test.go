package mediadelivery

import (
	"bytes"
	"callmemaybe/internal/mediaformat"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

type diskSource struct {
	*fakeSource
	payload []byte
}

func (s *diskSource) DownloadTo(_ context.Context, _ Offer, dst io.Writer) error {
	_, err := dst.Write(s.payload)
	return err
}

func TestAudiobookReceiveUsesPrivateDiskAndCleansEveryAttempt(t *testing.T) {
	for _, failure := range []string{"", "downloaded", "installing", "installed", "corrupt", "oversized"} {
		t.Run(failure, func(t *testing.T) {
			payload := bytes.Repeat([]byte("compressed"), 120000)
			s := &diskSource{fakeSource: fake(), payload: payload}
			s.offer.Bytes = int64(len(payload))
			s.offer.SHA256 = fmt.Sprintf("%x", sha256.Sum256(payload))
			if failure == "corrupt" {
				s.offer.SHA256 = strings.Repeat("0", 64)
			} else if failure == "oversized" {
				s.offer.Bytes--
			} else {
				s.failState = failure
			}
			dir := filepath.Join(t.TempDir(), "journal")
			installs := 0
			install := func(_ context.Context, path, hash string) error {
				installs++
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != 0600 || info.Size() != int64(len(payload)) {
					t.Fatal("not private disk staging", err)
				}
				return nil
			}
			err := ReceiveAudiobook(t.Context(), s, dir, install)
			if (failure == "") != (err == nil) {
				t.Fatal(failure, err)
			}
			files, _ := filepath.Glob(filepath.Join(dir, ".archive-*"))
			if len(files) != 0 {
				t.Fatal("private archive leaked")
			}
			if failure == "corrupt" || failure == "oversized" {
				if installs != 0 || s.offer.State != "failed" {
					t.Fatal("bad bytes installed")
				}
			}
			if failure == "" {
				if s.offer.State != "installed" || installs != 1 {
					t.Fatal("missing successful receipt")
				}
				if err := ReceiveAudiobook(t.Context(), s, dir, install); err != nil || installs != 1 {
					t.Fatal("reinstalled terminal book", err)
				}
			}
		})
	}
}

func TestAudiobookReceiveRemovesInterruptedArchiveWithoutFollowingLinks(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	interrupted := filepath.Join(dir, ".archive-interrupted")
	if err := os.WriteFile(interrupted, []byte("unfinished"), 0600); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(dir, "keep")
	if err := os.WriteFile(keep, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(keep, filepath.Join(dir, ".archive-link")); err != nil {
		t.Fatal(err)
	}
	payload := []byte("checked compressed bytes")
	source := &diskSource{fakeSource: fake(), payload: payload}
	source.offer.Bytes = int64(len(payload))
	source.offer.SHA256 = fmt.Sprintf("%x", sha256.Sum256(payload))
	if err := ReceiveAudiobook(t.Context(), source, dir, func(context.Context, string, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(interrupted); !os.IsNotExist(err) {
		t.Fatal("interrupted archive was retained")
	}
	data, err := os.ReadFile(keep)
	if err != nil || string(data) != "keep" {
		t.Fatal("removed unrelated data", err)
	}
	if info, err := os.Lstat(filepath.Join(dir, ".archive-link")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("followed staging symlink", err)
	}
}

type brokenDiskSource struct {
	*diskSource
	err error
}

func (s *brokenDiskSource) DownloadTo(context.Context, Offer, io.Writer) error { return s.err }
func TestAudiobookFailuresRetainTheirCauseCodes(t *testing.T) {
	for _, phase := range []string{"download-disk", "install-disk", "tools"} {
		t.Run(phase, func(t *testing.T) {
			payload := []byte("audio")
			base := &diskSource{fakeSource: fake(), payload: payload}
			base.offer.Bytes = int64(len(payload))
			base.offer.SHA256 = fmt.Sprintf("%x", sha256.Sum256(payload))
			var source FileSource = base
			failure := error(syscall.ENOSPC)
			want := "no_space"
			if phase == "download-disk" {
				source = &brokenDiskSource{base, failure}
			} else if phase == "tools" {
				failure = mediaformat.ErrUnsupported
				want = "unsupported"
			}
			err := ReceiveAudiobook(t.Context(), source, filepath.Join(t.TempDir(), "journal"), func(context.Context, string, string) error { return failure })
			if err == nil || base.offer.State != "failed" || base.codes[len(base.codes)-1] != want {
				t.Fatal("misclassified failure", base.codes, err)
			}
			if phase != "download-disk" && (!strings.Contains(err.Error(), "audiobook") || errors.Is(err, ErrChecksum)) {
				t.Fatal("preview-specific failure", err)
			}
		})
	}
}
