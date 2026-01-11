package main

import (
	"bytes"
	"fmt"
	"image"
	"math/bits"
	"sync"
	"time"

	"gioui.org/io/key"
	"golang.org/x/sys/windows"
)

var (
	onceWeaponNameLongest sync.Once
	weaponNameLongest     int
)

func shortcutListWeapons(key.Name, key.Modifiers) {
	const indexLength = 3

	weaponsMu.RLock()
	defer weaponsMu.RUnlock()

	onceWeaponNameLongest.Do(func() {
		for _, w := range weapons {
			weaponNameLongest = max(weaponNameLongest, len(w.Name))
		}
	})

	skipped := 0
	for i, w := range weapons {
		if w.SpeedMain == 0 {
			skipped++
			continue
		}

		speedMain, speedMainF, speedAlt, speedAltF := w.GetAllSpeeds(debugging)
		fmt.Fprintf(log.Out,
			"[%0*d] {%s_%s_%02d.%d_%02d.%d} %-*s %.2f%%\n",
			indexLength, i,
			w.Mode.string(true), w.Type.string(true),
			speedMain, speedMainF, speedAlt, speedAltF,
			weaponNameLongest, w.Name,
			w.Template.MaxVal*100,
		)
	}
	if skipped > 0 {
		log.Infof("skipped %d undefined weapon(s)", skipped)
	}
}

// Deprecated: there is a slight memory leak
func shortcutReloadWeapons(_ key.Name, mod key.Modifiers) {
	if mod.Contain(key.ModCtrl | key.ModShift) {
		loadTemplates()
	}
}

func shortcutPrintProcess(key.Name, key.Modifiers) {
	windowHandel = windows.GetForegroundWindow()
	log.Infof("parent process id: %d", parentProcessId)
	log.Infof("process id: %d", processId)
	log.Infof("window handel: %#X", windowHandel)
}

func shortcutResetFreamsElapsed(key.Name, key.Modifiers) {
	capturer.FramesElapsed = 0
	log.Info("capturer.FramesElapsed reset")
}

func shortcutToggleDraw(key.Name, key.Modifiers) {
	drawEnabled = !drawEnabled
}

func shortcutToggleDebug(key.Name, key.Modifiers) {
	debugging = !debugging
	log.Infof("debugging: %v", debugging)
	forceUpdate = true
}

var showPosTill time.Time

func shortcutMoveRoiRect(name key.Name, mod key.Modifiers) {
	boundaryCheck := func(constraints image.Rectangle, rect *image.Rectangle) {
		boundaryCheckPos := func(constraints image.Rectangle, pos *image.Point) {
			pos.X = max(pos.X, constraints.Min.X)
			pos.Y = max(pos.Y, constraints.Min.Y)
			pos.X = min(pos.X, constraints.Max.X)
			pos.Y = min(pos.Y, constraints.Max.Y)
		}
		size := rect.Size()
		boundaryCheckPos(constraints, &rect.Max)
		rect.Min = rect.Max.Sub(size)
		boundaryCheckPos(constraints, &rect.Min)
		rect.Max = rect.Min.Add(size)
	}

	offset := 1
	for range bits.OnesCount32(uint32(mod)) {
		offset *= 4
	}

	var newRect image.Rectangle

	switch name {
	case "R", "r":
		newRect = defaultRoiRect
	case key.NameUpArrow:
		newRect = roiRect.Sub(image.Pt(0, offset))
	case key.NameDownArrow:
		newRect = roiRect.Add(image.Pt(0, offset))
	case key.NameLeftArrow:
		newRect = roiRect.Sub(image.Pt(offset, 0))
	case key.NameRightArrow:
		newRect = roiRect.Add(image.Pt(offset, 0))
	}

	boundaryCheck(capturer.Bounds(), &newRect)
	roiRect = newRect
	showPosTill = time.Now().Add(time.Second * 3)
	log.Debugf("roiRect: %v", roiRect)
}

func shortcutSetWda(_ key.Name, mod key.Modifiers) {
	if windowHandel == 0 {
		windowHandel = windows.GetForegroundWindow()
	}

	currWda, err := GetWindowDisplayAffinity(windowHandel)
	if err != nil {
		log.Errorf("failed to GetWindowDisplayAffinity: %v", err)
		return
	}

	switch currWda {
	case WDA_NONE:
		var toWda uint32
		if !mod.Contain(key.ModShift) {
			toWda = WDA_EXCLUDEFROMCAPTURE
			log.Info("wda set to WDA_EXCLUDEFROMCAPTURE")
		} else {
			toWda = WDA_MONITOR
			log.Info("wda set to WDA_MONITOR")
		}
		err = SetWindowDisplayAffinity(windowHandel, toWda)
	case WDA_EXCLUDEFROMCAPTURE:
		log.Info("wda set to WDA_NONE")
		err = SetWindowDisplayAffinity(windowHandel, WDA_NONE)
	}

	if err != nil {
		log.Errorf("failed to SetWindowDisplayAffinity: %v", err)
	}
}

var (
	inputting      bool
	inputMainOrAlt bool // true for alt
	inputBuf       bytes.Buffer
)

func shortcutStartInput(k key.Name, m key.Modifiers) {
	switch k {
	case "I", "i": // start
		if !debugging {
			log.Warnf("not in debugging")
			return
		}
		inputting = true
		inputMainOrAlt = m.Contain(key.ModShift)
		return

	case key.NameDeleteBackward:
		if inputBuf.Len() == 0 {
			return
		}
		inputBuf.Truncate(inputBuf.Len() - 1)

	case key.NameReturn: // confirm
		if !inputting {
			return
		}
		inputting = false
		if inputBuf.Len() == 0 {
			log.Warn("empty input")
			return
		}
		modWeapon(inputMainOrAlt, inputBuf.String())
		inputBuf.Reset()

	case "0", "1", "2", "3", "4", "5", "6", "7", "8", "9", ".", "-":
		if inputting {
			inputBuf.WriteString(string(k))
		}
		return
	}
}
