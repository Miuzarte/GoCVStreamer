package widgets

import (
	"fmt"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op/clip"
)

type Shortcut struct {
	Keys []key.Name
	F    func(key.Name, key.Modifiers)
}

type Shortcuts struct {
	receiver     any
	eventFilters []event.Filter
	shortcuts    map[key.Name]Shortcut
}

func NewShortcut(f func(key.Name, key.Modifiers), keys ...key.Name) Shortcut {
	return Shortcut{
		Keys: keys,
		F:    f,
	}
}

// NewShortcuts does not allow multiple identical non-modifying keys
// cause it uses map for matching internally.
func NewShortcuts(receiver any, shortcuts ...Shortcut) (ss Shortcuts) {
	if len(shortcuts) == 0 {
		panic("no shortcut provided")
	}

	ss.receiver = receiver
	ss.eventFilters = []event.Filter{}
	ss.shortcuts = make(map[key.Name]Shortcut, len(shortcuts))
	for _, s := range shortcuts {
		for _, keyName := range s.Keys {
			ss.eventFilters = append(ss.eventFilters,
				key.Filter{
					Optional: key.ModCtrl |
						key.ModCommand |
						key.ModShift |
						key.ModAlt |
						key.ModSuper,
					Name: keyName,
				},
			)

			if _, ok := ss.shortcuts[keyName]; ok {
				panic(fmt.Errorf("repeated key: %s", keyName))
			}
			ss.shortcuts[keyName] = s
		}
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
			shortcut, ok := ss.shortcuts[e.Name]
			if !ok {
				continue
			}
			shortcut.F(e.Name, e.Modifiers)

		default:
			return fmt.Errorf("unknown key event[%T]: %v", event, event)
		}
	}

	return nil
}
