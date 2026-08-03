package wgc

import (
	"fmt"
	"strings"
	"unsafe"

	"github.com/shirou/gopsutil/v4/process"
	"golang.org/x/sys/windows"
)

var (
	moduser32                    = windows.NewLazySystemDLL("user32.dll")
	procEnumWindows              = moduser32.NewProc("EnumWindows")
	procGetWindowThreadProcessId = moduser32.NewProc("GetWindowThreadProcessId")
	procGetWindowTextLengthW     = moduser32.NewProc("GetWindowTextLengthW")
	procGetWindowTextW           = moduser32.NewProc("GetWindowTextW")
	procIsWindowVisible          = moduser32.NewProc("IsWindowVisible")
	procIsIconic                 = moduser32.NewProc("IsIconic")
)

// FindWindow 按进程名或窗口标题查找可见、非最小化的窗口。
// 优先返回前台窗口；否则返回枚举到的第一个匹配窗口。
// procNames 与 titleSubstr 至少提供一个；进程名大小写不敏感，标题为大小写不敏感的子串匹配。
func FindWindow(procNames []string, titleSubstr string) (windows.HWND, error) {
	if len(procNames) == 0 && titleSubstr == "" {
		return 0, fmt.Errorf("window lookup: process name or title required")
	}

	foreground := windows.GetForegroundWindow()
	var best windows.HWND
	bestIsForeground := false

	cb := windows.NewCallback(func(hwnd windows.HWND, _ uintptr) uintptr {
		if visible, _, _ := procIsWindowVisible.Call(uintptr(hwnd)); visible == 0 {
			return 1
		}
		if iconic, _, _ := procIsIconic.Call(uintptr(hwnd)); iconic != 0 {
			return 1
		}
		if !windowMatches(hwnd, procNames, titleSubstr) {
			return 1
		}
		if hwnd == foreground {
			best = hwnd
			bestIsForeground = true
			return 0 // stop enumeration
		}
		if !bestIsForeground && best == 0 {
			best = hwnd
		}
		return 1
	})

	procEnumWindows.Call(cb, 0)
	if best == 0 {
		return 0, fmt.Errorf("no visible window matched (process=%v title=%q)", procNames, titleSubstr)
	}
	return best, nil
}

func windowMatches(hwnd windows.HWND, procNames []string, titleSubstr string) bool {
	var pid uint32
	procGetWindowThreadProcessId.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&pid)))

	if pid != 0 && len(procNames) > 0 {
		p, err := process.NewProcess(int32(pid))
		if err == nil {
			if name, err := p.Name(); err == nil {
				for _, n := range procNames {
					if strings.EqualFold(name, n) {
						return true
					}
				}
			}
		}
	}

	if titleSubstr != "" {
		n, _, _ := procGetWindowTextLengthW.Call(uintptr(hwnd))
		if n > 0 {
			buf := make([]uint16, n+1)
			procGetWindowTextW.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
			title := windows.UTF16ToString(buf)
			if strings.Contains(strings.ToLower(title), strings.ToLower(titleSubstr)) {
				return true
			}
		}
	}
	return false
}
