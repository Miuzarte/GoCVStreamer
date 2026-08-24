package main

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	"github.com/shirou/gopsutil/v4/process"
	"golang.org/x/sys/windows"
)

// assistForegroundAllowed 为 true 时允许 assist 工作 (前台窗口是目标游戏)
var assistForegroundAllowed atomic.Bool

// gameProcessNames 返回当前 game 模式匹配的前台进程名 (大小写不敏感)
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
		// 未知 game 模式不启用前台门控
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

// foregroundGameLoop 每秒检查一次前台窗口进程名, 匹配才允许 assist
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

// targetProcessLoop 周期性检查目标游戏进程; 持续 timeout 未检测到则退出自身
// names 为目标进程名列表 (gameProcessNames), timeout 为检测超时, cancel 用于触发整体退出
func targetProcessLoop(ctx context.Context, names []string, timeout time.Duration, cancel context.CancelFunc) {
	const interval = 5 * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// 启动时视为已检测到一次, 给足超时窗口
	lastSeen := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		if anyProcessRunning(names) {
			lastSeen = time.Now()
			continue
		}
		if since := time.Since(lastSeen); since >= timeout {
			log.Warn().
				Strs("processes", names).
				Dur("timeout", timeout).
				Dur("undetectedFor", since).
				Msg("target game process not detected, exiting")
			cancel()
			return
		}
		log.Debug().
			Strs("processes", names).
			Dur("undetectedFor", time.Since(lastSeen)).
			Dur("timeout", timeout).
			Msg("target game process not detected yet")
	}
}

// anyProcessRunning 枚举所有进程, 任一名字与 names 匹配 (大小写不敏感) 即视为检测到
// 枚举失败时保守返回 true, 避免误退出
func anyProcessRunning(names []string) bool {
	procs, err := process.Processes()
	if err != nil {
		log.Warn().Err(err).Msg("failed to enumerate processes, assuming target running")
		return true
	}
	for _, p := range procs {
		name, err := p.Name()
		if err != nil {
			continue
		}
		for _, n := range names {
			if strings.EqualFold(name, n) {
				return true
			}
		}
	}
	return false
}
