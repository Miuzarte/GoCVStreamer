// Command capturebench 对 DXGI/WGC 两条采集路径做无下游处理的
// 延迟与阶段耗时测量：帧间隔、GetImage 总耗时、各阶段耗时、
// 端到端帧龄（present/合成 -> GetImage 返回）与积压指标。
package main

import (
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"image"
	"os"
	"sort"
	"strconv"
	"time"
	"unsafe"

	"github.com/Miuzarte/GoCVStreamer/capturer"
	"github.com/Miuzarte/GoCVStreamer/wgc"
	"github.com/kirides/go-d3d/outputduplication"
	"golang.org/x/sys/windows"
)

var (
	kernel32    = windows.NewLazySystemDLL("kernel32.dll")
	procQPC     = kernel32.NewProc("QueryPerformanceCounter")
	procQPCFreq = kernel32.NewProc("QueryPerformanceFrequency")

	user32                    = windows.NewLazySystemDLL("user32.dll")
	procGetWindowThreadProcId = user32.NewProc("GetWindowThreadProcessId")
	procGetWindowTextLengthW  = user32.NewProc("GetWindowTextLengthW")
	procGetWindowTextW        = user32.NewProc("GetWindowTextW")
)

var (
	source     = flag.String("source", "wgc", "capture source: dxgi or wgc")
	display    = flag.Int("display", 0, "display index")
	window     = flag.String("window", "", "wgc window target (process name/title, or auto=foreground)")
	seconds    = flag.Int("seconds", 30, "measurement duration (s)")
	warmup     = flag.Float64("warmup", 2.0, "warmup duration (s), stats discarded")
	timeoutMs  = flag.Uint("timeout-ms", 1000, "per-frame capture timeout (ms)")
	csvPath    = flag.String("csv", "", "write per-frame samples to this CSV file")
	borderless = flag.Bool("borderless", true, "hide the WGC yellow border")
)

type sample struct {
	frameID        uint64
	cadenceUs      int64
	totalUs        int64
	ageUs          int64
	acquireUs      int64
	metadataUs     int64
	copyUs         int64
	mapUs          int64
	rowcopyUs      int64
	swizzleUs      int64
	trygetUs       int64
	waitUs         int64
	copyoutUs      int64
	framesReceived uint64
	framesReturned uint64
	framesMissed   uint64
	sysQPC         int64
	arrivedQPC     int64
	ageArrivalUs   int64
}

func main() {
	flag.Parse()
	if *source != "dxgi" && *source != "wgc" {
		fmt.Fprintf(os.Stderr, "invalid -source %q (want dxgi or wgc)\n", *source)
		os.Exit(2)
	}
	if *source == "dxgi" && *window != "" {
		fmt.Fprintln(os.Stderr, "window capture is only supported with -source wgc")
		os.Exit(2)
	}

	src, err := openSource()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create capture source: %v\n", err)
		os.Exit(1)
	}
	defer src.Close()

	var freq int64
	procQPCFreq.Call(uintptr(unsafe.Pointer(&freq)))
	qpcNow := func() int64 {
		var c int64
		procQPC.Call(uintptr(unsafe.Pointer(&c)))
		return c
	}

	buf := image.NewRGBA(src.Bounds())
	ws, _ := src.(*wgc.WgcSource)

	fmt.Printf("bench start source=%s bounds=%dx%d timeout=%dms warmup=%.1fs measure=%ds\n",
		*source, buf.Bounds().Dx(), buf.Bounds().Dy(), *timeoutMs, *warmup, *seconds)
	printForeground()

	var (
		samples      []sample
		idleWaits    int
		sizeChanges  int
		capErrors    int
		measured     int
		lastFrameQPC int64
	)

	start := time.Now()
	warmupEnd := start.Add(time.Duration(*warmup * float64(time.Second)))
	measureEnd := warmupEnd.Add(time.Duration(*seconds) * time.Second)
	lastTick := start

	for {
		now := time.Now()
		if now.After(measureEnd) {
			break
		}
		if now.Sub(lastTick) >= 5*time.Second {
			fmt.Printf("[bench] elapsed=%.0fs frames=%d\n", now.Sub(start).Seconds(), measured)
			lastTick = now
		}

		t0 := time.Now()
		err := src.GetImageTimeout(buf, *timeoutMs)
		totalUs := time.Since(t0).Microseconds()
		nowQPC := qpcNow()

		switch {
		case errors.Is(err, outputduplication.ErrNoImageYet):
			idleWaits++
			continue
		case errors.Is(err, capturer.ErrSizeChanged):
			sizeChanges++
			buf = image.NewRGBA(src.Bounds())
			continue
		case err != nil:
			capErrors++
			fmt.Fprintf(os.Stderr, "capture error: %v\n", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}

		if now.Before(warmupEnd) {
			lastFrameQPC = nowQPC
			continue
		}

		s := sample{
			frameID: uint64(src.FramesElapsed()),
			totalUs: totalUs,
		}
		if lastFrameQPC != 0 {
			s.cadenceUs = (nowQPC - lastFrameQPC) * 1000000 / freq
		}
		lastFrameQPC = nowQPC

		if ws != nil {
			p, ok := ws.LastPerf()
			if ok && p.SystemQPC != 0 {
				s.ageUs = usBetween(nowQPC, p.SystemQPC, freq)
			}
			if ok && p.ArrivedQPC != 0 {
				s.ageArrivalUs = usBetween(nowQPC, p.ArrivedQPC, freq)
			}
			s.sysQPC = p.SystemQPC
			s.arrivedQPC = p.ArrivedQPC
			s.trygetUs = p.TryGetUs
			s.copyUs = p.CopyUs
			s.mapUs = p.MapUs
			s.rowcopyUs = p.RowCopyUs
			s.waitUs = p.WaitUs
			s.copyoutUs = p.CopyOutUs
			s.framesReceived = p.FramesReceived
			s.framesReturned = p.FramesReturned
			s.framesMissed = p.FramesMissed
		}

		samples = append(samples, s)
		measured++
	}

	printSummary(*source, samples, measured, measureEnd.Sub(warmupEnd), idleWaits, sizeChanges, capErrors, ws)
	writeCSV(*source, samples)
}

// printForeground 输出当前前台窗口，用于判断 NVIDIA 后台帧率限制是否可能作用于内容源。
func printForeground() {
	hwnd := windows.GetForegroundWindow()
	if hwnd == 0 {
		fmt.Println("foreground window: none")
		return
	}
	var pid uint32
	procGetWindowThreadProcId.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&pid)))

	title := ""
	if n, _, _ := procGetWindowTextLengthW.Call(uintptr(hwnd)); n > 0 {
		buf := make([]uint16, n+1)
		procGetWindowTextW.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		title = windows.UTF16ToString(buf)
	}
	fmt.Printf("foreground window hwnd=0x%X pid=%d title=%q\n", uint64(hwnd), pid, title)
}

func openSource() (capturer.Source, error) {
	if *source == "dxgi" {
		fmt.Printf("opening dxgi display=%d\n", *display)
		return capturer.New(*display)
	}

	wgc.SetBorderless(*borderless)
	if *window != "" {
		var hwnd windows.HWND
		if *window == "auto" {
			hwnd = windows.GetForegroundWindow()
			if hwnd == 0 {
				return nil, errors.New("no foreground window for -window auto")
			}
		} else {
			h, err := wgc.FindWindow([]string{*window}, *window)
			if err != nil {
				return nil, err
			}
			hwnd = h
		}
		fmt.Printf("opening wgc window hwnd=0x%X clientArea=true\n", uint64(hwnd))
		return wgc.NewWindowSource(hwnd, true, nil)
	}

	fmt.Printf("opening wgc display=%d\n", *display)
	return wgc.NewDisplaySource(*display)
}

func usBetween(nowQPC, thenQPC, freq int64) int64 {
	if thenQPC == 0 || freq == 0 {
		return -1
	}
	us := (nowQPC - thenQPC) * 1000000 / freq
	if us < 0 {
		us = 0
	}
	return us
}

func printSummary(source string, samples []sample, measured int, duration time.Duration,
	idleWaits, sizeChanges, capErrors int, ws *wgc.WgcSource) {

	fps := 0.0
	if duration > 0 {
		fps = float64(measured) / duration.Seconds()
	}
	fmt.Printf("\nresult source=%s duration=%.1fs frames=%d fps=%.1f idle_waits=%d size_changes=%d errors=%d\n",
		source, duration.Seconds(), measured, fps, idleWaits, sizeChanges, capErrors)

	metrics := []struct {
		name string
		vals []int64
	}{
		{"cadence", pick(samples, func(s sample) int64 { return s.cadenceUs })},
		{"total", pick(samples, func(s sample) int64 { return s.totalUs })},
		{"age", pick(samples, func(s sample) int64 { return s.ageUs })},
		{"age_arrival", pick(samples, func(s sample) int64 { return s.ageArrivalUs })},
		{"acquire", pick(samples, func(s sample) int64 { return s.acquireUs })},
		{"metadata", pick(samples, func(s sample) int64 { return s.metadataUs })},
		{"copy", pick(samples, func(s sample) int64 { return s.copyUs })},
		{"map", pick(samples, func(s sample) int64 { return s.mapUs })},
		{"rowcopy", pick(samples, func(s sample) int64 { return s.rowcopyUs })},
		{"swizzle", pick(samples, func(s sample) int64 { return s.swizzleUs })},
		{"tryget", pick(samples, func(s sample) int64 { return s.trygetUs })},
		{"wait", pick(samples, func(s sample) int64 { return s.waitUs })},
		{"copyout", pick(samples, func(s sample) int64 { return s.copyoutUs })},
	}

	fmt.Printf("%-12s %8s %10s %10s %10s %10s %10s\n",
		"metric", "count", "avg_us", "p50_us", "p95_us", "p99_us", "max_us")
	for _, m := range metrics {
		if len(m.vals) == 0 {
			continue
		}
		sorted := append([]int64(nil), m.vals...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		var sum int64
		for _, v := range sorted {
			sum += v
		}
		if sum == 0 {
			continue
		}
		fmt.Printf("%-12s %8d %10d %10d %10d %10d %10d\n",
			m.name, len(sorted), sum/int64(len(sorted)),
			pct(sorted, 0.50), pct(sorted, 0.95), pct(sorted, 0.99), sorted[len(sorted)-1])
	}

	if ws != nil && len(samples) > 0 {
		last := samples[len(samples)-1]
		backlog := int64(last.framesReceived - last.framesReturned)
		if backlog < 0 {
			backlog = 0
		}
		fmt.Printf("wgc backlog(received-returned)=%d frames_missed=%d\n", backlog, last.framesMissed)
	}
}

func pick(samples []sample, f func(sample) int64) []int64 {
	out := make([]int64, 0, len(samples))
	for _, s := range samples {
		out = append(out, f(s))
	}
	return out
}

func pct(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * p)
	if idx < 0 {
		idx = 0
	}
	return sorted[idx]
}

func writeCSV(source string, samples []sample) {
	if *csvPath == "" {
		return
	}
	f, err := os.Create(*csvPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "csv: %v\n", err)
		return
	}
	defer f.Close()

	cw := csv.NewWriter(f)
	cw.Write([]string{
		"source", "frame_id", "cadence_us", "total_us", "age_us",
		"acquire_us", "metadata_us", "copy_us", "map_us", "rowcopy_us",
		"swizzle_us", "tryget_us", "wait_us", "copyout_us",
		"frames_received", "frames_returned", "frames_missed",
		"system_qpc", "arrived_qpc",
	})
	for _, s := range samples {
		cw.Write([]string{
			source,
			strconv.FormatUint(s.frameID, 10),
			strconv.FormatInt(s.cadenceUs, 10),
			strconv.FormatInt(s.totalUs, 10),
			strconv.FormatInt(s.ageUs, 10),
			strconv.FormatInt(s.acquireUs, 10),
			strconv.FormatInt(s.metadataUs, 10),
			strconv.FormatInt(s.copyUs, 10),
			strconv.FormatInt(s.mapUs, 10),
			strconv.FormatInt(s.rowcopyUs, 10),
			strconv.FormatInt(s.swizzleUs, 10),
			strconv.FormatInt(s.trygetUs, 10),
			strconv.FormatInt(s.waitUs, 10),
			strconv.FormatInt(s.copyoutUs, 10),
			strconv.FormatUint(s.framesReceived, 10),
			strconv.FormatUint(s.framesReturned, 10),
			strconv.FormatUint(s.framesMissed, 10),
			strconv.FormatInt(s.sysQPC, 10),
			strconv.FormatInt(s.arrivedQPC, 10),
		})
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		fmt.Fprintf(os.Stderr, "csv: %v\n", err)
		return
	}
	fmt.Printf("csv written to %s (%d rows)\n", *csvPath, len(samples))
}
