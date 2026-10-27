package books

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Menu audio is independent of recording revisions and handset bookmarks.
// Calls pin an immutable bundle; only the offline builder invokes synthesis.
type menuBundle struct {
	Schema  int               `json:"schema"`
	Catalog string            `json:"catalog"`
	Voice   string            `json:"voice"`
	Clips   map[string]string `json:"clips"`
}

func digest(v any) string {
	data, _ := json.Marshal(v)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func pageKey(root, kind string, titles []string, page int) string {
	relative := make([]string, len(titles))
	for i, path := range titles {
		relative[i], _ = filepath.Rel(root, path)
	}
	return fmt.Sprintf("page/%s/%s/%d", kind, digest(relative), page)
}

// say obeys embedded [[...]] commands, so a title such as "[[slnc 60000]]"
// would turn a menu page into dead air. Spacing every bracket means no two can
// ever form a command delimiter, whatever their run length.
func speakable(title string) string {
	return strings.NewReplacer("[", "[ ", "]", " ]").Replace(title)
}

// menuTexts is every clip a bundle holds. shelves are public lines' shelves
// (#155), each a list of title ids: their pages are rendered beside the
// catalog's, keyed by the shelf's own titles like every page.
func menuTexts(root string, c Catalog, shelves ...[]string) map[string]string {
	texts := make(map[string]string)
	for name, text := range prompts {
		texts[name] = text
	}
	texts["tip"] = "Two for faster. Eight for slower. Star for all controls."
	for speed := 5; speed <= 20; speed++ {
		texts[fmt.Sprintf("speed-%d", speed)] = fmt.Sprintf("Speed, %d point %d times.", speed/10, speed%10)
	}
	pages := func(kind string, titles, names []string) {
		count := (len(names) + 8) / 9
		for page := 0; page < count; page++ {
			heading := "Audiobooks."
			if kind == "chapters" {
				heading = "Chapters."
			}
			if count > 1 {
				heading += fmt.Sprintf(" Page %d of %d.", page+1, count)
			}
			for i := page * 9; i < len(names) && i < (page+1)*9; i++ {
				heading += fmt.Sprintf(" For %s, press %d.", names[i], i-page*9+1)
			}
			if count > 1 {
				if page+1 < count {
					heading += " Pound for the next page."
				} else {
					heading += " Pound to choose a page."
				}
			}
			if count > 1 {
				heading += " Star then pound to choose a page."
			}
			heading += " Star to repeat. Zero to go back."
			texts[pageKey(root, kind, titles, page)] = heading
		}
	}
	var titles, names []string
	for _, b := range c.Books {
		path := filepath.Join(bookDir(root, b), "title")
		relative, _ := filepath.Rel(root, path)
		text := speakable(b.Title) + "."
		if b.Preview {
			text += " Preview recording."
		}
		texts[relative] = text
		titles = append(titles, path)
		names = append(names, speakable(b.Title))
		var chapters, chapterNames []string
		for i, ch := range b.Chapters {
			chapters = append(chapters, filepath.Join(bookDir(root, b), chapterName(i)+"-title"))
			chapterNames = append(chapterNames, speakable(ch.Title))
		}
		pages("chapters", chapters, chapterNames)
	}
	pages("books", titles, names)
	for _, shelf := range shelves {
		var titles, names []string
		for _, b := range shelfOf(c, shelf) {
			titles = append(titles, filepath.Join(bookDir(root, b), "title"))
			names = append(names, speakable(b.Title))
		}
		pages("books", titles, names)
	}
	return texts
}

// PrepareMenus also works with a copy of catalog.json alone, so a Mac can
// render menus for a Linux box without copying or touching its recordings.
// shelves are the [[shelves]] public lines offer, so their pages speak in
// the same voice; without them a shelf is announced with the compatibility
// prompts, which is complete but plainer.
func PrepareMenus(ctx context.Context, root, voice string, shelves ...[]string) error {
	if !validText(voice) {
		return errors.New("invalid menu voice")
	}
	c, err := Load(root)
	if err != nil {
		return err
	}
	if err := checkVoice(ctx, voice); err != nil {
		return err
	}
	return buildMenus(root, c, voice, func(text, output string) error {
		return sayMenu(ctx, voice, text, output)
	}, shelves...)
}

func buildMenus(root string, c Catalog, voice string, render func(string, string) error, shelves ...[]string) error {
	dir := filepath.Join(root, "menu-audio")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	// A render interrupted by a signal or crash leaves its folder behind.
	stale, _ := filepath.Glob(filepath.Join(dir, ".render-*"))
	for _, path := range stale {
		_ = os.RemoveAll(path)
	}
	bundle := menuBundle{Schema: 1, Catalog: digest(c), Voice: voice, Clips: map[string]string{}}
	for key, text := range menuTexts(root, c, shelves...) {
		hash := digest([]string{"macos-say-v1-loudnorm-18", voice, text})
		destination := filepath.Join(dir, hash+".sln16")
		s, err := os.Lstat(destination)
		if err != nil || !s.Mode().IsRegular() || s.Size() == 0 || s.Size()%2 != 0 {
			tmp, err := os.MkdirTemp(dir, ".render-")
			if err != nil {
				return err
			}
			output := filepath.Join(tmp, "clip.sln16")
			err = render(text, output)
			if err == nil {
				s, err = os.Lstat(output)
				if err == nil && (!s.Mode().IsRegular() || s.Size() == 0 || s.Size()%2 != 0) {
					err = errors.New("invalid menu PCM")
				}
			}
			if err == nil {
				err = os.Chmod(output, 0644)
			}
			if err == nil {
				err = syncFile(output)
			}
			if err == nil {
				err = os.Rename(output, destination)
			}
			_ = os.RemoveAll(tmp)
			if err != nil {
				return err
			}
		}
		bundle.Clips[key] = hash
	}
	return publishJSON(filepath.Join(root, "menus.json"), bundle, 0644)
}

func toolError(err error, tool, notFound, failed string) error {
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("%s not found (%s): %w", tool, notFound, err)
	}
	return fmt.Errorf("%s: %w", failed, err)
}

// say silently substitutes the default voice for an unknown name, which would
// publish a bundle labelled with a voice it does not contain.
func checkVoice(ctx context.Context, voice string) error {
	out, err := exec.CommandContext(ctx, "say", "-v", "?").Output()
	if err != nil {
		return toolError(err, "say", "macOS only", "listing macOS say voices failed")
	}
	for _, name := range voiceNames(string(out)) {
		if strings.EqualFold(name, voice) {
			return nil
		}
	}
	return fmt.Errorf("voice %q is not installed; `say -v '?'` lists installed voices", voice)
}

// Each `say -v '?'` line is "<name> <locale> # <sample>". Names can contain
// spaces ("Eddy (English (UK))"), so the locale is the last field before '#'.
func voiceNames(list string) []string {
	var names []string
	for _, line := range strings.Split(list, "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		i := strings.LastIndexAny(line, " \t")
		if i < 0 {
			continue
		}
		if name := strings.TrimSpace(line[:i]); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func sayMenu(ctx context.Context, voice, text, output string) error {
	source := output + ".aiff"
	defer os.Remove(source)
	cmd := exec.CommandContext(ctx, "say", "-v", voice, "-r", "170", "-o", source)
	cmd.Stdin = strings.NewReader(text)
	if err := cmd.Run(); err != nil {
		return toolError(err, "say", "macOS only", "macOS say failed")
	}
	cmd = exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error", "-y", "-i", source, "-af", "loudnorm=I=-18:TP=-2:LRA=7", "-ar", "16000", "-ac", "1", "-f", "s16le", output)
	if err := cmd.Run(); err != nil {
		return toolError(err, "ffmpeg", "install FFmpeg", "menu audio conversion failed")
	}
	return nil
}

// Without the sync a power loss after the rename could leave an immutable,
// content-addressed name pointing at a truncated clip.
func syncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	err = f.Sync()
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

// retireStaleMenus moves a bundle built for another catalog aside after a new
// catalog is published, so the box stops attempting it and the operator can
// see why the macOS voice went away. The rename is atomic; calls that already
// loaded the old bundle keep their pinned clips.
func retireStaleMenus(root string) error {
	path := filepath.Join(root, "menus.json")
	var bundle menuBundle
	if err := readJSON(path, &bundle); os.IsNotExist(err) {
		return nil
	} else if err == nil {
		if c, err := Load(root); err == nil && bundle.Schema == 1 && bundle.Catalog == digest(c) {
			return nil
		}
	}
	return os.Rename(path, path+".stale")
}

func loadMenus(root string, c Catalog) (map[string]string, error) {
	var bundle menuBundle
	err := readJSON(filepath.Join(root, "menus.json"), &bundle)
	if os.IsNotExist(err) {
		return nil, nil
	} // Existing prepared libraries remain usable.
	if err != nil {
		// Fixed text: decoder errors can quote bundle content.
		return nil, errors.New("unreadable menus.json")
	}
	if bundle.Schema != 1 || bundle.Catalog != digest(c) {
		return nil, errors.New("rebuild menus for the current catalog")
	}
	clips := make(map[string]string)
	for key := range menuTexts(root, c) {
		hash := bundle.Clips[key]
		if !revisionPattern.MatchString(hash) {
			return nil, errors.New("invalid menu clip")
		}
		path := filepath.Join(root, "menu-audio", hash)
		s, err := os.Lstat(path + ".sln16")
		if err != nil || !s.Mode().IsRegular() || s.Size() == 0 || s.Size()%2 != 0 {
			return nil, errors.New("missing menu audio")
		}
		clips[key] = path
	}
	// Shelf pages are extras: a bundle built without them is complete, and
	// one damaged page costs that page its voice, never the bundle. Keyed by
	// the shelf's titles, a page can only ever announce the list it was
	// rendered for.
	for key, hash := range bundle.Clips {
		if _, required := clips[key]; required || !strings.HasPrefix(key, "page/books/") || !revisionPattern.MatchString(hash) {
			continue
		}
		path := filepath.Join(root, "menu-audio", hash)
		if s, err := os.Lstat(path + ".sln16"); err == nil && s.Mode().IsRegular() && s.Size() > 0 && s.Size()%2 == 0 {
			clips[key] = path
		}
	}
	return clips, nil
}

func (m *menu) title(path string) (int, error) {
	key, _ := filepath.Rel(m.root, path)
	if clip := m.clips[key]; clip != "" {
		path = clip
	}
	return m.a.play(path)
}
