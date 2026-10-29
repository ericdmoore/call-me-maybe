package books

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// A menu holds one shared lease for its whole immutable list. Reaping never
// removes a title/audio file an active menu or player could still reference.
func podcastPlaybackLock(root string, exclusive bool) (func(), error) {
	if err := os.MkdirAll(root, 0750); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(root, ".playback.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0660)
	if err != nil {
		return nil, err
	}
	mode := syscall.LOCK_SH
	if exclusive {
		mode = syscall.LOCK_EX | syscall.LOCK_NB
	}
	if err = syscall.Flock(int(f.Fd()), mode); err != nil {
		f.Close()
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}
func ReapPodcasts(root string) error {
	if err := os.MkdirAll(root, 0750); err != nil {
		return err
	}
	unlock, err := lockLibrary(root)
	if err != nil {
		return err
	}
	defer unlock()
	release, err := podcastPlaybackLock(root, true)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return nil
	}
	if err != nil {
		return err
	}
	defer release()
	c, err := Load(root)
	if os.IsNotExist(err) {
		c = Catalog{Schema: 1}
	} else if err != nil {
		return err
	}
	live := map[string]bool{}
	for _, b := range c.Books {
		live[filepath.Base(bookDir(root, b))] = true
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		split := strings.LastIndex(name, "-")
		edition := split > 0 && revisionPattern.MatchString(name[split+1:]) && strings.HasPrefix(name, "pod-")
		if entry.IsDir() && !live[name] && (edition || strings.HasPrefix(name, ".prepare-")) {
			if err = os.RemoveAll(filepath.Join(root, name)); err != nil {
				return err
			}
		}
	}
	return nil
}
