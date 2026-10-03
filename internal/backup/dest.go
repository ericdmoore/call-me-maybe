package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Destination is somewhere a bundle goes. The file backend is here; s3 and
// the account follow the same four verbs so `backup` and `restore` never
// know which they are talking to.
type Destination interface {
	// Name is "file", "s3", "cloud" — for the plan, the journal and check.
	Name() string
	Put(ctx context.Context, name string, r io.Reader) error
	Get(ctx context.Context, name string) (io.ReadCloser, error)
	// List returns bundle names, any order; Prune decides what stays.
	List(ctx context.Context) ([]string, error)
	Delete(ctx context.Context, name string) error
}

// FileDest is a directory: on the box, a mounted drive, an NFS share.
type FileDest struct{ Dir string }

func (f FileDest) Name() string { return "file" }

func (f FileDest) Put(_ context.Context, name string, r io.Reader) error {
	if err := os.MkdirAll(f.Dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(f.Dir, "."+name+".tmp-*")
	if err != nil {
		return err
	}
	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(f.Dir, name))
}

func (f FileDest) Get(_ context.Context, name string) (io.ReadCloser, error) {
	return os.Open(filepath.Join(f.Dir, filepath.Base(name)))
}

func (f FileDest) List(_ context.Context) ([]string, error) {
	names, err := filepath.Glob(filepath.Join(f.Dir, "callmemaybe-*.age"))
	if err != nil {
		return nil, err
	}
	for i := range names {
		names[i] = filepath.Base(names[i])
	}
	return names, nil
}

func (f FileDest) Delete(_ context.Context, name string) error {
	err := os.Remove(filepath.Join(f.Dir, filepath.Base(name)))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

var bundleName = regexp.MustCompile(`^callmemaybe-(.+)-(\d{8}T\d{6}Z)\.age$`)

// StampOf reads the time out of a bundle name.
func StampOf(name string) (time.Time, bool) {
	_, t, ok := parseName(name)
	return t, ok
}

// parseName splits a bundle name into its host and time.
func parseName(name string) (host string, at time.Time, ok bool) {
	m := bundleName.FindStringSubmatch(filepath.Base(name))
	if m == nil {
		return "", time.Time{}, false
	}
	t, err := time.Parse("20060102T150405Z", m[2])
	return m[1], t, err == nil
}

// Latest is the newest bundle among names, by its stamp.
func Latest(names []string) (string, bool) {
	best, ok := "", false
	var bestAt time.Time
	for _, n := range names {
		at, valid := StampOf(n)
		if valid && (!ok || at.After(bestAt)) {
			best, bestAt, ok = n, at, true
		}
	}
	return best, ok
}

// Keep decides which of ONE host's bundles stay under a daily/weekly rule:
// the newest `daily` by day, plus the newest in each of the last `weekly`
// ISO weeks. Only that host's bundles are considered at all — another
// house sharing the destination keeps its own history under its own
// settings, and names that are not bundles are never touched.
func Keep(host string, names []string, daily, weekly int, now time.Time) (keep, prune []string) {
	type b struct {
		name string
		host string
		at   time.Time
	}
	var all []b
	for _, n := range names {
		if h, at, ok := parseName(n); ok && h == host {
			all = append(all, b{n, h, at})
		}
	}
	{
		sort.Slice(all, func(i, j int) bool { return all[i].at.After(all[j].at) })
		kept := map[string]bool{}
		days := map[string]bool{}
		for _, x := range all {
			d := x.at.UTC().Format("2006-01-02")
			if len(days) < daily && !days[d] {
				days[d] = true
				kept[x.name] = true
			}
		}
		weeks := map[string]bool{}
		for _, x := range all {
			y, w := x.at.UTC().ISOWeek()
			k := fmt.Sprintf("%d-%02d", y, w)
			if len(weeks) < weekly && !weeks[k] {
				weeks[k] = true
				kept[x.name] = true
			}
		}
		for _, x := range all {
			if kept[x.name] {
				keep = append(keep, x.name)
			} else {
				prune = append(prune, x.name)
			}
		}
	}
	return keep, prune
}

// Deliver puts one bundle to every destination and prunes each, reporting
// per destination so one failure never hides another's success.
type Delivery struct {
	Dest   string
	Err    error
	Pruned int
}

func Deliver(ctx context.Context, dests []Destination, name string, content []byte, daily, weekly int, now time.Time) []Delivery {
	host, _, _ := parseName(name)
	var out []Delivery
	for _, d := range dests {
		r := Delivery{Dest: d.Name()}
		if err := d.Put(ctx, name, strings.NewReader(string(content))); err != nil {
			r.Err = err
			out = append(out, r)
			continue
		}
		names, err := d.List(ctx)
		if err != nil {
			r.Err = fmt.Errorf("delivered, but could not list for pruning: %w", err)
			out = append(out, r)
			continue
		}
		_, prune := Keep(host, names, daily, weekly, now)
		for _, p := range prune {
			if err := d.Delete(ctx, p); err != nil {
				r.Err = fmt.Errorf("delivered, but could not prune %s: %w", p, err)
				break
			}
			r.Pruned++
		}
		out = append(out, r)
	}
	return out
}
