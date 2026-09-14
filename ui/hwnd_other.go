//go:build !windows

package ui

import "gioui.org/io/event"

// viewHWND 在非 Windows 后端上没有对应概念, 永远当作"不是视图事件"。
func viewHWND(event.Event) (uintptr, bool) { return 0, false }
