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

func menuTexts(root string, c Catalog) map[string]string {
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
			heading += " Star to repeat. Zero to go back."
			texts[pageKey(root, kind, titles, page)] = heading
		}
	}
	var titles, names []string
	for _, b := range c.Books {
		path := filepath.Join(bookDir(root, b), "title")
		relative, _ := filepath.Rel(root, path)
		text := b.Title + "."
		if b.Preview {
			text += " Preview recording."
		}
		texts[relative] = text
		titles = append(titles, path)
		names = append(names, b.Title)
		var chapters, chapterNames []string
		for i, ch := range b.Chapters {
			chapters = append(chapters, filepath.Join(bookDir(root, b), chapterName(i)+"-title"))
			chapterNames = append(chapterNames, ch.Title)
		}
		pages("chapters", chapters, chapterNames)
	}
	pages("books", titles, names)
	return texts
}

// PrepareMenus also works with a copy of catalog.json alone, so a Mac can
// render menus for a Linux box without copying or touching its recordings.
func PrepareMenus(ctx context.Context, root, voice string) error {
	if !validText(voice) {
		return errors.New("invalid menu voice")
	}
	c, err := Load(root)
	if err != nil {
		return err
	}
	return buildMenus(root, c, voice, func(text, output string) error {
		return sayMenu(ctx, voice, text, output)
	})
}

func buildMenus(root string, c Catalog, voice string, render func(string, string) error) error {
	dir := filepath.Join(root, "menu-audio")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	bundle := menuBundle{Schema: 1, Catalog: digest(c), Voice: voice, Clips: map[string]string{}}
	for key, text := range menuTexts(root, c) {
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

func sayMenu(ctx context.Context, voice, text, output string) error {
	source := output + ".aiff"
	defer os.Remove(source)
	cmd := exec.CommandContext(ctx, "say", "-v", voice, "-r", "170", "-o", source)
	cmd.Stdin = strings.NewReader(text)
	if err := cmd.Run(); err != nil {
		return errors.New("macOS say failed; check the installed voice")
	}
	cmd = exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error", "-y", "-i", source, "-af", "loudnorm=I=-18:TP=-2:LRA=7", "-ar", "16000", "-ac", "1", "-f", "s16le", output)
	if err := cmd.Run(); err != nil {
		return errors.New("menu audio conversion failed; check FFmpeg")
	}
	return nil
}

func loadMenus(root string, c Catalog) (map[string]string, error) {
	var bundle menuBundle
	err := readJSON(filepath.Join(root, "menus.json"), &bundle)
	if os.IsNotExist(err) {
		return nil, nil
	} // Existing prepared libraries remain usable.
	if err != nil {
		return nil, err
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
	return clips, nil
}

func (m *menu) title(path string) (int, error) {
	key, _ := filepath.Rel(m.root, path)
	if clip := m.clips[key]; clip != "" {
		path = clip
	}
	return m.a.play(path)
}
