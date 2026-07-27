package mouse

import (
	"syscall"
	"unsafe"
)

const (
	INPUT_MOUSE = 0
	sizeofINPUT = 40

	MOUSEEVENTF_MOVE       = 0x0001
	MOUSEEVENTF_LEFTDOWN   = 0x0002
	MOUSEEVENTF_LEFTUP     = 0x0004
	MOUSEEVENTF_RIGHTDOWN  = 0x0008
	MOUSEEVENTF_RIGHTUP    = 0x0010
	MOUSEEVENTF_MIDDLEDOWN = 0x0020
	MOUSEEVENTF_MIDDLEUP   = 0x0040

	MB_LEFT   = 0
	MB_MIDDLE = 1
	MB_RIGHT  = 2
)

type MOUSEINPUT struct {
	Dx          int32
	Dy          int32
	MouseData   uint32
	DwFlags     uint32
	Time        uint32
	_           uint32
	DwExtraInfo uintptr
}

type INPUT struct {
	Type uint32
	_    uint32
	Mi   MOUSEINPUT
}

var (
	user32        = syscall.MustLoadDLL("user32.dll")
	procSendInput = user32.MustFindProc("SendInput")
)

func send(input *INPUT) error {
	r, _, e := syscall.SyscallN(procSendInput.Addr(), 1, uintptr(unsafe.Pointer(input)), sizeofINPUT)
	if r == 0 {
		return e
	}
	return nil
}

func Move(dx, dy int) error {
	return send(&INPUT{
		Type: INPUT_MOUSE,
		Mi: MOUSEINPUT{
			Dx:      int32(dx),
			Dy:      int32(dy),
			DwFlags: MOUSEEVENTF_MOVE,
		},
	})
}

var (
	downFlags = [3]uint32{MOUSEEVENTF_LEFTDOWN, MOUSEEVENTF_MIDDLEDOWN, MOUSEEVENTF_RIGHTDOWN}
	upFlags   = [3]uint32{MOUSEEVENTF_LEFTUP, MOUSEEVENTF_MIDDLEUP, MOUSEEVENTF_RIGHTUP}
)

func MouseDown(button int) error {
	if button < 0 || button > 2 {
		return nil
	}
	return send(&INPUT{
		Type: INPUT_MOUSE,
		Mi: MOUSEINPUT{
			DwFlags: downFlags[button],
		},
	})
}

func MouseUp(button int) error {
	if button < 0 || button > 2 {
		return nil
	}
	return send(&INPUT{
		Type: INPUT_MOUSE,
		Mi: MOUSEINPUT{
			DwFlags: upFlags[button],
		},
	})
}

func MouseClick(button int) error {
	if button < 0 || button > 2 {
		return nil
	}
	inputs := [2]INPUT{
		{Type: INPUT_MOUSE, Mi: MOUSEINPUT{DwFlags: downFlags[button]}},
		{Type: INPUT_MOUSE, Mi: MOUSEINPUT{DwFlags: upFlags[button]}},
	}
	r, _, e := syscall.SyscallN(procSendInput.Addr(), 2, uintptr(unsafe.Pointer(&inputs[0])), sizeofINPUT)
	if r != 2 {
		return e
	}
	return nil
}
