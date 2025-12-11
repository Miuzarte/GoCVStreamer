package widgets

import (
	"fmt"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op/clip"
)

type shortcutFunc func(key.Name, key.Modifiers)

type Shortcut struct {
	Keys []key.Name
	F    []shortcutFunc
}

func NewShortcut(keys ...key.Name) *Shortcut {
	return &Shortcut{
		Keys: keys,
	}
}

func (s *Shortcut) Do(f ...func(key.Name, key.Modifiers)) *Shortcut {
	s.F = make([]shortcutFunc, 0, len(f))
	for _, g := range f {
		s.F = append(s.F, shortcutFunc(g))
	}
	return s
}

type Shortcuts struct {
	receiver     any
	eventFilters []event.Filter
	shortcutFuns map[key.Name][]shortcutFunc
}

// NewShortcuts does not allow multiple identical non-modifying keys
// cause it uses map for matching internally.
func NewShortcuts(receiver any, shortcuts ...*Shortcut) (ss Shortcuts) {
	if len(shortcuts) == 0 {
		panic("no shortcut provided")
	}

	ss.receiver = receiver

	keysSet := map[key.Name]struct{}{}
	ss.shortcutFuns = make(map[key.Name][]shortcutFunc, len(shortcuts))
	for _, s := range shortcuts {
		for _, keyName := range s.Keys {
			keysSet[keyName] = struct{}{}
			shortcus := ss.shortcutFuns[keyName]
			shortcus = append(shortcus, s.F...)
			ss.shortcutFuns[keyName] = shortcus
		}
	}

	const allOptional = key.ModCtrl | key.ModCommand |
		key.ModShift | key.ModAlt | key.ModSuper
	for keyName := range keysSet {
		ss.eventFilters = append(ss.eventFilters,
			key.Filter{
				Optional: allOptional,
				Name:     keyName,
			},
		)
	}

	return
}

func (ss *Shortcuts) Match(gtx layout.Context) error {
	area := clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops)
	defer area.Pop()
	event.Op(gtx.Ops, ss.receiver)

	for {
		event, ok := gtx.Event(ss.eventFilters...)
		if !ok {
			break
		}
		switch e := event.(type) {
		case key.Event:
			if e.State != key.Press {
				continue
			}
			for _, f := range ss.shortcutFuns[e.Name] {
				f(e.Name, e.Modifiers)
			}

		default:
			return fmt.Errorf("unknown key event[%T]: %v", event, event)
		}
	}

	return nil
}
