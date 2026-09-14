//go:build windows

package ui

import (
	"gioui.org/app"
	"gioui.org/io/event"
)

// viewHWND 从 Gio 的事件流里取出本窗口的 HWND。
//
// Windows 后端在窗口创建后发一次 app.Win32ViewEvent (app/os_windows.go:114),
// 窗口销毁时发零值表示句柄作废。
//
// 用它而不是 FindWindowW 按标题去找: 后者在同时跑两个实例、或者标题被改过时会找错;
// 而 SetWindowDisplayAffinity 这类 API 只对本进程的窗口有效, 找错了不只是无效,
// 还会静默地什么都不做。
//
// 返回 ok=false 表示这个事件不是视图事件。
func viewHWND(e event.Event) (hwnd uintptr, ok bool) {
	v, isView := e.(app.Win32ViewEvent)
	if !isView {
		return 0, false
	}
	if !v.Valid() {
		return 0, true // 视图失效: 句柄作废, 用 0 通知调用方
	}
	return v.HWND, true
}
