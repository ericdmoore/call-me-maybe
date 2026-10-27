package pack

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

var prefixLine = regexp.MustCompile(`(?m)^[ \t]*PROMPT_MEDIA_PREFIX[ \t]*=.*$`)

// Use selects verified, immutable audio. reload belongs to the host adapter;
// this package neither controls Asterisk nor requires a running phone system.
// A failed reload restores the old file and attempts to restore the old
// service configuration. It never reports a failed switch as active.
func (s Store) Use(id, envPath string, reload func() error) (*Installation, error) {
	unlock, err := s.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	info, err := s.Installed(id)
	if err != nil {
		return nil, err
	}
	if info.Kind != KindVoice {
		return nil, errors.New("pack: only a voice pack can be the default lobby voice")
	}
	st, err := os.Lstat(envPath)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New("pack: environment file must be a regular file, not a symlink")
	}
	old, err := os.ReadFile(envPath)
	if err != nil {
		return nil, err
	}
	line := "PROMPT_MEDIA_PREFIX=" + info.Prefix
	next := prefixLine.ReplaceAllLiteral(old, []byte(line))
	if !prefixLine.Match(old) {
		next = []byte(strings.TrimRight(string(old), "\n") + "\n" + line + "\n")
	}
	if err = writeEnv(envPath, next, st); err != nil {
		return nil, err
	}
	if reload != nil {
		if err = reload(); err != nil {
			current, e := os.ReadFile(envPath)
			if e != nil || !bytes.Equal(current, next) {
				return nil, errors.New("pack: service reload failed and the environment file changed; automatic rollback refused, check the configuration")
			}
			if e = writeEnv(envPath, old, st); e != nil {
				return nil, fmt.Errorf("pack: reload failed; restoring the previous environment failed: %w", e)
			}
			if e = reload(); e != nil {
				return nil, errors.New("pack: reload failed; previous configuration restored, but the service still needs attention")
			}
			return nil, errors.New("pack: reload failed; restored the previous configuration and service")
		}
	}
	return info, nil
}

func writeEnv(dest string, data []byte, st os.FileInfo) error {
	f, err := os.CreateTemp(filepath.Dir(dest), ".pack-env-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if stat, ok := st.Sys().(*syscall.Stat_t); ok {
		err = f.Chown(int(stat.Uid), int(stat.Gid))
	}
	if err == nil {
		err = f.Chmod(st.Mode().Perm())
	}
	if err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), dest)
}
