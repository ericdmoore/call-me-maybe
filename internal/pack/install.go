package pack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"callmemaybe/internal/voice"
)

const managedPrefix = "doorman-packs"

// Store keeps immutable playback versions separate from private build state.
// Installing never changes an active prefix, including on upgrade of one id.
type Store struct{ SoundsDir string }

type Progress struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	Completed int       `json:"completed"`
	Total     int       `json:"total"`
	Clip      string    `json:"clip,omitempty"`
	Problem   string    `json:"problem,omitempty"`
	Updated   time.Time `json:"updated"`
}

type Provenance struct {
	Name             string            `json:"name"`
	Origin           string            `json:"origin"`
	SourceHash       string            `json:"source_hash,omitempty"`
	RendererRevision string            `json:"renderer_revision,omitempty"`
	Backend          string            `json:"backend,omitempty"`
	Voice            string            `json:"voice,omitempty"`
	Created          time.Time         `json:"created"`
	Files            map[string]string `json:"files"`
}

type Installation struct {
	ID      string       `json:"id"`
	Name    string       `json:"name"`
	Version string       `json:"version"`
	Kind    string       `json:"kind"`
	Prefix  string       `json:"prefix"`
	Archive string       `json:"archive_sha256"`
	Clips   []Provenance `json:"clips"`
}

type Entry struct {
	ID        string        `json:"id"`
	Installed *Installation `json:"installed,omitempty"`
	Attempt   *Progress     `json:"attempt,omitempty"`
}

type InstallOptions struct {
	Renderer         func(string) (voice.Renderer, error)
	RendererRevision string
	Progress         func(Progress)
}

type InstallPlan struct {
	Total   int    `json:"total"`
	Bundled int    `json:"bundled"`
	Cached  int    `json:"cached"`
	Render  []Item `json:"render"`
}

func (s Store) private() string { return filepath.Join(s.SoundsDir, ".doorman-packs") }
func (s Store) installedPath(id string) string {
	return filepath.Join(s.private(), id+".installed.json")
}
func (s Store) progressPath(id string) string { return filepath.Join(s.private(), id+".progress.json") }
func cacheKey(item Item, revision string) string {
	h := sha256.Sum256([]byte(item.Name + "\x00" + item.hash() + "\x00" + revision))
	return hex.EncodeToString(h[:])
}
func (s Store) cachePath(item Item, revision string) string {
	return filepath.Join(s.private(), "cache", cacheKey(item, revision))
}

// Plan has no filesystem writes and never creates a renderer.
func (s Store) Plan(a *Artifact, revision string) InstallPlan {
	p := InstallPlan{Total: len(a.Pack.Items)}
	for _, item := range a.Pack.Items {
		if _, ok := a.Pack.Manifest.Audio[item.Name]; ok {
			p.Bundled++
			continue
		}
		if _, err := cached(s.cachePath(item, revision), item, revision); err == nil {
			p.Cached++
			continue
		}
		p.Render = append(p.Render, item)
	}
	return p
}

func cached(dir string, item Item, revision string) (Provenance, error) {
	var p Provenance
	if err := readJSON(dir, "provenance.json", &p); err != nil {
		return p, err
	}
	if p.Name != item.Name || p.SourceHash != item.hash() || p.RendererRevision != revision || p.Origin != "rendered" {
		return p, errors.New("pack: stale cache")
	}
	return p, validateProvenance(dir, p)
}

func validateProvenance(dir string, p Provenance) error {
	if !safeName(p.Name) || len(p.Files) != 2 {
		return errors.New("pack: invalid audio provenance")
	}
	for _, rate := range []int{8000, 16000} {
		name := p.Name + ext(rate)
		b, err := readRegular(dir, name, maxEntryBytes)
		if err != nil {
			return err
		}
		h := sha256.Sum256(b)
		if p.Files[name] != hex.EncodeToString(h[:]) {
			return errors.New("pack: installed/cached audio checksum mismatch")
		}
		if _, err := validateWAV(b, rate); err != nil {
			return err
		}
	}
	return nil
}

func provenance(dir string, item Item, source AudioSource, revision string) (Provenance, error) {
	p := Provenance{Name: item.Name, Origin: source.Origin, SourceHash: source.SourceHash, Created: time.Now().UTC(), Files: map[string]string{}}
	if source.Origin == "rendered" {
		p.Backend = item.Voice.Backend
		p.Voice = item.Voice.ID
		p.RendererRevision = revision
	}
	for _, rate := range []int{8000, 16000} {
		name := item.Name + ext(rate)
		b, err := readRegular(dir, name, maxEntryBytes)
		if err != nil {
			return p, err
		}
		h := sha256.Sum256(b)
		p.Files[name] = hex.EncodeToString(h[:])
	}
	return p, validateProvenance(dir, p)
}

// Lock serialises installers and activation. flock releases on a crashed
// process; a stale directory must not make the next install impossible.
func (s Store) Lock() (func(), error) {
	if err := os.MkdirAll(s.SoundsDir, 0o755); err != nil {
		return nil, err
	}
	if err := plainDir(s.private(), 0o700); err != nil {
		return nil, err
	}
	name := filepath.Join(s.private(), "lock")
	if st, err := os.Lstat(name); err == nil && !st.Mode().IsRegular() {
		return nil, errors.New("pack: unsafe lock file")
	}
	f, err := os.OpenFile(name, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("pack: another install or activation is running")
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}

func plainDir(name string, mode os.FileMode) error {
	if err := os.Mkdir(name, mode); err != nil && !os.IsExist(err) {
		return err
	}
	st, err := os.Lstat(name)
	if err != nil {
		return err
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return errors.New("pack: managed path is not a plain directory")
	}
	return nil
}

func (s Store) Install(ctx context.Context, a *Artifact, opts InstallOptions) (_ *Installation, err error) {
	unlock, err := s.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	p := a.Pack
	progress := Progress{ID: p.Manifest.ID, Status: "staged", Total: len(p.Items)}
	report := func() error {
		progress.Updated = time.Now().UTC()
		if err := writeJSON(s.progressPath(p.Manifest.ID), progress, 0o600); err != nil {
			return err
		}
		if opts.Progress != nil {
			opts.Progress(progress)
		}
		return nil
	}
	if err = report(); err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			progress.Status = "failed"
			progress.Problem = err.Error()
			_ = report()
		}
	}()
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	for _, dir := range []string{filepath.Join(s.private(), "cache"), filepath.Join(s.SoundsDir, managedPrefix), filepath.Join(s.SoundsDir, managedPrefix, p.Manifest.ID), filepath.Join(s.SoundsDir, managedPrefix, p.Manifest.ID, "versions")} {
		mode := os.FileMode(0o755)
		if strings.HasPrefix(dir, s.private()+string(filepath.Separator)) {
			mode = 0o700
		}
		if err = plainDir(dir, mode); err != nil {
			return nil, err
		}
	}
	// Check all required backends before spending anything. The factory may
	// inspect operator configuration, but no Render is called during preflight.
	renderers := map[string]voice.Renderer{}
	plan := s.Plan(a, opts.RendererRevision)
	for _, item := range plan.Render {
		if _, ok := renderers[item.Voice.Backend]; ok {
			continue
		}
		if opts.Renderer == nil {
			return nil, errors.New("pack: rendering needs a configured voice backend")
		}
		r, e := opts.Renderer(item.Voice.Backend)
		if e != nil || r == nil {
			return nil, fmt.Errorf("pack: renderer %q unavailable; check its credentials, executable and model", item.Voice.Backend)
		}
		renderers[item.Voice.Backend] = r
	}
	stage, err := os.MkdirTemp(filepath.Join(s.SoundsDir, managedPrefix, p.Manifest.ID), ".install-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	info := &Installation{ID: p.Manifest.ID, Name: p.Manifest.Name, Version: p.Manifest.Version, Kind: p.Manifest.Kind, Archive: a.Digest}
	for _, item := range p.Items {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		var record Provenance
		if source, bundled := p.Manifest.Audio[item.Name]; bundled {
			if err = copyAudio(p.Dir, stage, item.Name); err != nil {
				return nil, err
			}
			record, err = provenance(stage, item, source, "")
		} else {
			progress.Status = "rendering"
			progress.Clip = item.Name
			if err = report(); err != nil {
				return nil, err
			}
			cache := s.cachePath(item, opts.RendererRevision)
			record, err = cached(cache, item, opts.RendererRevision)
			if err != nil {
				record, err = s.render(ctx, item, opts, renderers[item.Voice.Backend])
			}
			if err == nil {
				err = copyAudio(cache, stage, item.Name)
			}
		}
		if err != nil {
			return nil, err
		}
		info.Clips = append(info.Clips, record)
		progress.Completed++
		progress.Clip = item.Name
		if err = report(); err != nil {
			return nil, err
		}
	}
	if p.Story != nil {
		if err = p.writeGraph(stage); err != nil {
			return nil, err
		}
		name := storyFilename(p.Manifest)
		source, e := readRegular(p.Dir, name, maxSourceBytes)
		if e != nil {
			return nil, e
		}
		dest := filepath.Join(stage, filepath.FromSlash(name))
		if err = os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return nil, err
		}
		if err = os.WriteFile(dest, source, 0o644); err != nil {
			return nil, err
		}
	}
	raw, err := json.MarshalIndent(p.Manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	if err = os.WriteFile(filepath.Join(stage, "pack.json"), raw, 0o644); err != nil {
		return nil, err
	}
	// Content includes renderer revision and exact audio digests. Installed
	// versions are immutable so an in-flight call keeps the same audio files.
	stable := append([]Provenance(nil), info.Clips...)
	for i := range stable {
		stable[i].Created = time.Time{}
	}
	identity, _ := json.Marshal(stable)
	hash := sha256.Sum256(append([]byte(a.Digest), identity...))
	name := hex.EncodeToString(hash[:])
	info.Prefix = pathPrefix(p.Manifest.ID, name)
	dest := filepath.Join(s.SoundsDir, filepath.FromSlash(info.Prefix))
	if err = writeJSON(filepath.Join(stage, "installation.json"), info, 0o644); err != nil {
		return nil, err
	}
	if err = os.Chmod(stage, 0o755); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if _, e := os.Lstat(dest); e == nil {
		// The complete directory may precede its registry entry after a crash.
		if err = s.verify(info); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(e) {
		return nil, e
	} else if err = os.Rename(stage, dest); err != nil {
		return nil, err
	}
	if err = writeJSON(s.installedPath(info.ID), info, 0o600); err != nil {
		return nil, err
	}
	progress.Status = "installed"
	progress.Clip = ""
	if err = report(); err != nil {
		return nil, err
	}
	return info, nil
}

func pathPrefix(id, revision string) string {
	return managedPrefix + "/" + id + "/versions/" + revision
}

func (s Store) render(ctx context.Context, item Item, opts InstallOptions, r voice.Renderer) (Provenance, error) {
	var zero Provenance
	dir, err := os.MkdirTemp(filepath.Join(s.private(), "cache"), ".render-")
	if err != nil {
		return zero, err
	}
	defer os.RemoveAll(dir)
	one := Pack{Items: []Item{item}}
	if _, err := one.Build(ctx, dir, func(string) (voice.Renderer, error) { return r, nil }); err != nil {
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		// A backend's error can contain response text or subprocess stderr. Those
		// are not safe to persist or print from a downloaded pack's install job.
		return zero, fmt.Errorf("pack: rendering %q failed (%s); check provider availability, credentials and voice configuration", item.Name, item.Voice.Backend)
	}
	record, err := provenance(dir, item, AudioSource{Origin: "rendered", SourceHash: item.hash()}, opts.RendererRevision)
	if err != nil {
		return zero, err
	}
	if err = writeJSON(filepath.Join(dir, "provenance.json"), record, 0o600); err != nil {
		return zero, err
	}
	dest := s.cachePath(item, opts.RendererRevision)
	// An invalid cache is disposable, never active playback. The store lock
	// prevents another installer from consuming it while it is replaced.
	if err = os.RemoveAll(dest); err != nil {
		return zero, err
	}
	if err = os.Rename(dir, dest); err != nil {
		return zero, err
	}
	return record, nil
}

func (s Store) verify(info *Installation) error {
	if !packID.MatchString(info.ID) || !strings.HasPrefix(info.Prefix, managedPrefix+"/"+info.ID+"/versions/") || !safeName(info.Prefix) || len(strings.Split(info.Prefix, "/")) != 4 {
		return errors.New("pack: invalid installation path")
	}
	dir := filepath.Join(s.SoundsDir, filepath.FromSlash(info.Prefix))
	for _, p := range info.Clips {
		if err := validateProvenance(dir, p); err != nil {
			return err
		}
	}
	if info.Kind == KindVoice {
		names := map[string]bool{}
		for _, p := range info.Clips {
			names[p.Name] = true
		}
		if len(names) != len(PromptNames) {
			return errors.New("pack: installed voice is incomplete")
		}
		for _, name := range PromptNames {
			if !names[name] {
				return errors.New("pack: installed voice is incomplete")
			}
		}
	}
	return nil
}

// Installed verifies the selected version instead of trusting its registry.
func (s Store) Installed(id string) (*Installation, error) {
	if !packID.MatchString(id) {
		return nil, errors.New("pack: invalid id")
	}
	var info Installation
	if err := readJSON(s.private(), id+".installed.json", &info); err != nil {
		return nil, err
	}
	if info.ID != id {
		return nil, errors.New("pack: installation id mismatch")
	}
	if err := s.verify(&info); err != nil {
		return nil, err
	}
	return &info, nil
}

func (s Store) List() ([]Entry, error) {
	files, err := os.ReadDir(s.private())
	if os.IsNotExist(err) {
		return []Entry{}, nil
	}
	if err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	for _, f := range files {
		for _, suffix := range []string{".installed.json", ".progress.json"} {
			if strings.HasSuffix(f.Name(), suffix) {
				ids[strings.TrimSuffix(f.Name(), suffix)] = true
			}
		}
	}
	names := make([]string, 0, len(ids))
	for id := range ids {
		names = append(names, id)
	}
	sort.Strings(names)
	entries := make([]Entry, 0, len(names))
	for _, id := range names {
		e := Entry{ID: id}
		var info Installation
		var progress Progress
		if err := readJSON(s.private(), id+".installed.json", &info); err == nil {
			e.Installed = &info
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if err := readJSON(s.private(), id+".progress.json", &progress); err == nil {
			e.Attempt = &progress
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, nil
}

func readJSON(dir, name string, v any) error {
	b, err := readRegular(dir, name, maxEntryBytes)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
func writeJSON(dest string, v any, mode os.FileMode) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return atomicFile(dest, append(b, '\n'), mode)
}
