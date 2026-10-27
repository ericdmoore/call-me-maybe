package phonegame

import (
	"errors"
	"sort"
	"strconv"
	"strings"

	"go.starlark.net/starlark"
)

// Answer positions are pinned per room, and only answer windows translate keys.
// Confirmation and social-menu keys retain their advertised meanings.
func (r *room) prepareLayouts() {
	r.layouts = map[string]map[string]string{}
	if !r.m.pack.ShuffleAnswers {
		return
	}
	for _, q := range r.m.pack.Questions {
		layout := map[string]string{}
		used := map[string]bool{}
		for key, button := range q.Pins {
			layout[key] = button
			used[button] = true
		}
		keys, buttons := []string{}, []string{}
		for _, key := range []string{"1", "2", "3", "4"} {
			if _, ok := layout[key]; !ok {
				keys = append(keys, key)
			}
			if !used[key] {
				buttons = append(buttons, key)
			}
		}
		for i := len(buttons) - 1; i > 0; i-- {
			j := pick(i + 1)
			buttons[i], buttons[j] = buttons[j], buttons[i]
		}
		for i, key := range keys {
			layout[key] = buttons[i]
		}
		r.m.mu.Lock()
		previous := r.m.previousLayouts[q.ID]
		same := previous != nil
		for key, button := range layout {
			if previous[key] != button {
				same = false
			}
		}
		if same && len(keys) > 1 {
			a, b := keys[0], keys[1]
			layout[a], layout[b] = layout[b], layout[a]
		}
		r.m.previousLayouts[q.ID] = layout
		r.m.mu.Unlock()
		r.layouts[q.ID] = layout
	}
}
func (r *room) activeLayout(reply *Reply) map[string]string {
	deck, _, _ := reply.State.Get(starlark.String("deck"))
	list, ok := deck.(*starlark.List)
	if !ok {
		return nil
	}
	index := getInt(reply.State, "rung")
	if index < 0 || index >= list.Len() {
		return nil
	}
	id, _ := starlark.AsString(list.Index(index))
	return r.layouts[id]
}
func (r *room) display(reply *Reply) ([]Command, error) {
	commands := append([]Command(nil), reply.Commands...)
	if !r.m.pack.ShuffleAnswers {
		return commands, nil
	}
	for i, c := range commands {
		if c.Op == "say" {
			for _, q := range r.m.pack.Questions {
				prefix := q.Prefix + ".choice"
				if strings.HasPrefix(c.Line, prefix) {
					key := strings.TrimPrefix(c.Line, prefix)
					if len(key) != 1 {
						return nil, errors.New("invalid choice line")
					}
					button := r.layouts[q.ID][key]
					variant := c.Line + ".key" + button
					if _, ok := r.m.pack.Clips[variant]; !ok {
						return nil, errors.New("missing shuffled choice audio")
					}
					commands[i].Line = variant
					break
				}
			}
		}
		if c.Op == "ask_digit" && getString(reply.State, "phase") == "answer" {
			mapping := r.activeLayout(reply)
			keys := ""
			for _, key := range c.Keys {
				if button, ok := mapping[string(key)]; ok {
					keys += button
				} else {
					keys += string(key)
				}
			}
			commands[i].Keys = keys
		}
	}
	for i := 0; i < len(commands); {
		end := i
		for end < len(commands) && strings.Contains(commands[end].Line, ".choice") && strings.Contains(commands[end].Line, ".key") {
			end++
		}
		if end > i {
			sort.Slice(commands[i:end], func(a, b int) bool {
				return commands[i+a].Line[len(commands[i+a].Line)-1] < commands[i+b].Line[len(commands[i+b].Line)-1]
			})
			i = end
		} else {
			i++
		}
	}
	return commands, nil
}
func (r *room) canonical(reply *Reply, key *string) *string {
	if key == nil || getString(reply.State, "phase") != "answer" {
		return key
	}
	for canonical, button := range r.activeLayout(reply) {
		if button == *key {
			return &canonical
		}
	}
	return key
}
func validateLayout(p *Pack, q Question) error {
	if !p.ShuffleAnswers {
		return nil
	}
	if !name.MatchString(q.Prefix) {
		return errors.New("invalid question prefix")
	}
	used := map[string]bool{}
	for key, button := range q.Pins {
		if !strings.Contains("1234", key) || len(key) != 1 || !strings.Contains("1234", button) || len(button) != 1 || used[button] {
			return errors.New("invalid numeric button pins")
		}
		used[button] = true
	}
	for i := 1; i <= 4; i++ {
		for j := 1; j <= 4; j++ {
			if _, ok := p.Clips[q.Prefix+".choice"+strconv.Itoa(i)+".key"+strconv.Itoa(j)]; !ok {
				return errors.New("missing shuffled answer variant")
			}
		}
	}
	return nil
}
