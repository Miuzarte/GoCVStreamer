package main

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	"github.com/shirou/gopsutil/v4/process"
	"golang.org/x/sys/windows"
)

// assistForegroundAllowed 为 true 时允许 assist 工作（前台窗口是目标游戏）。
var assistForegroundAllowed atomic.Bool

// gameProcessNames 返回当前 game 模式匹配的前台进程名（大小写不敏感）。
func gameProcessNames(game string) []string {
	switch game {
	case "cs2":
		return []string{"cs2", "cs2.exe"}
	case "r6s":
		return []string{"rainbowsix", "rainbowsix.exe"}
	}
	return nil
}

func foregroundGameActive(names []string) bool {
	if len(names) == 0 {
		// 未知 game 模式不启用前台门控。
		return true
	}

	hwnd := windows.GetForegroundWindow()
	if hwnd == 0 {
		return false
	}

	var pid uint32
	windows.GetWindowThreadProcessId(hwnd, &pid)
	if pid == 0 {
		return false
	}

	p, err := process.NewProcess(int32(pid))
	if err != nil {
		return false
	}
	name, err := p.Name()
	if err != nil {
		return false
	}

	for _, n := range names {
		if strings.EqualFold(name, n) {
			return true
		}
	}
	return false
}

// foregroundGameLoop 每秒检查一次前台窗口进程名，匹配才允许 assist。
func foregroundGameLoop(ctx context.Context, names []string) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	last := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		active := foregroundGameActive(names)
		assistForegroundAllowed.Store(active)
		if active != last {
			last = active
			log.Info().
				Bool("active", active).
				Msg("assist auto toggle")
		}
	}
}
