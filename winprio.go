package main

import (
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// GPU 调度优先级类 (D3DKMT_SCHEDULINGPRIORITYCLASS)
//
// 这是 WDDM 进程级的调度类, 决定本进程的 GPU 工作相对其它进程的排队次序
// 只有开了 "硬件加速 GPU 计划" (HAGS) 才有意义; 与 CPU 优先级 (main.init 里的
// HIGH_PRIORITY_CLASS) 无关
const (
	gpuPriorityIdle        = 0
	gpuPriorityBelowNormal = 1
	gpuPriorityNormal      = 2
	gpuPriorityAboveNormal = 3
	gpuPriorityHigh        = 4
	gpuPriorityRealtime    = 5
)

// gpuPriorityNames 命令行名字 -> 优先级类, 供 -gpu-priority 使用
var gpuPriorityNames = map[string]int{
	"idle":     gpuPriorityIdle,
	"below":    gpuPriorityBelowNormal,
	"normal":   gpuPriorityNormal,
	"above":    gpuPriorityAboveNormal,
	"high":     gpuPriorityHigh,
	"realtime": gpuPriorityRealtime,
}

var (
	modGdi32 = windows.NewLazySystemDLL("gdi32.dll")

	procGetGPUProcessPriority = modGdi32.NewProc("D3DKMTGetProcessSchedulingPriorityClass")
	procSetGPUProcessPriority = modGdi32.NewProc("D3DKMTSetProcessSchedulingPriorityClass")
)

// getGPUProcessPriority 查询进程的 GPU 调度优先级类, 返回 NTSTATUS 非 0 即失败
func getGPUProcessPriority(h windows.Handle) (int, error) {
	var class int32
	status, _, _ := procGetGPUProcessPriority.Call(uintptr(h), uintptr(unsafe.Pointer(&class)))
	if status != 0 {
		return 0, fmt.Errorf("D3DKMTGetProcessSchedulingPriorityClass: NTSTATUS 0x%08X", uint32(status))
	}
	return int(class), nil
}

// setGPUProcessPriority 设置进程的 GPU 调度优先级类, 返回 NTSTATUS 非 0 即失败
func setGPUProcessPriority(h windows.Handle, class int) error {
	status, _, _ := procSetGPUProcessPriority.Call(uintptr(h), uintptr(class))
	if status != 0 {
		return fmt.Errorf("D3DKMTSetProcessSchedulingPriorityClass: NTSTATUS 0x%08X", uint32(status))
	}
	return nil
}

func gpuPriorityClassName(class int) string {
	for name, c := range gpuPriorityNames {
		if c == class {
			return name
		}
	}
	return fmt.Sprintf("unknown(%d)", class)
}

// hagsState 读取 "硬件加速 GPU 计划" 状态 (2 = 已启用, 1 = 已关闭), 读不到返回 0
func hagsState() uint32 {
	k, err := registry.OpenKey(
		registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Control\GraphicsDrivers`,
		registry.QUERY_VALUE,
	)
	if err != nil {
		return 0
	}
	defer k.Close()

	v, _, err := k.GetIntegerValue("HwSchMode")
	if err != nil {
		return 0
	}
	return uint32(v)
}

// logGPUPriorityState 打印本进程当前的 GPU 调度优先级类与 HAGS 状态 (只读)
func logGPUPriorityState(tag string) {
	class, err := getGPUProcessPriority(windows.CurrentProcess())
	if err != nil {
		log.Warn().
			Str("tag", tag).
			Err(err).
			Msg("failed to query GPU scheduling priority class")
		return
	}

	hags := hagsState()
	ev := log.Info().
		Str("tag", tag).
		Str("gpuPriority", gpuPriorityClassName(class))
	if hags == 0 {
		ev.Str("hags", "unknown")
	} else {
		ev.Uint32("hags", hags)
	}
	ev.Msg("GPU scheduling priority class")

	if hags == 1 {
		log.Debug().
			Msg("hardware-accelerated GPU scheduling is off, GPU priority class may have no effect")
	}
}

// initGPUPriority 应用 -gpu-priority 指定的 GPU 调度优先级类
//
// normal 表示不改动本进程; 其余取值在设置前后各打一次读回日志, 失败只告警
func initGPUPriority(name string) {
	class, ok := gpuPriorityNames[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		log.Warn().
			Str("gpuPriority", name).
			Msg("unknown GPU priority class, ignored")
		return
	}
	if class == gpuPriorityNormal {
		logGPUPriorityState("startup")
		return
	}

	logGPUPriorityState("before-set")
	if err := setGPUProcessPriority(windows.CurrentProcess(), class); err != nil {
		log.Warn().
			Str("gpuPriority", name).
			Err(err).
			Msg("failed to set GPU scheduling priority class")
		return
	}
	logGPUPriorityState("after-set")
}
