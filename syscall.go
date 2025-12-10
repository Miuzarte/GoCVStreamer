package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	WDA_NONE               = 0x00000000
	WDA_MONITOR            = 0x00000001
	WDA_EXCLUDEFROMCAPTURE = 0x00000011
)

var (
	moduser32                    = windows.NewLazySystemDLL("user32.dll")
	procSetWindowDisplayAffinity = moduser32.NewProc("SetWindowDisplayAffinity")
	procGetWindowDisplayAffinity = moduser32.NewProc("GetWindowDisplayAffinity")
)

func SetWindowDisplayAffinity(hWnd windows.HWND, dwAffinity uint32) error {
	ret, _, err := procSetWindowDisplayAffinity.Call(
		uintptr(hWnd),
		uintptr(dwAffinity),
	)

	if ret == 0 {
		return err
	}

	return nil
}

func GetWindowDisplayAffinity(hWnd windows.HWND) (dwAffinity uint32, _ error) {
	ret, _, err := procGetWindowDisplayAffinity.Call(
		uintptr(hWnd),
		uintptr(unsafe.Pointer(&dwAffinity)),
	)

	if ret == 0 {
		return 0, err
	}

	return dwAffinity, nil
}
