package keystate

import (
	"syscall"
)

var (
	user32               = syscall.MustLoadDLL("user32.dll")
	procGetAsyncKeyState = user32.MustFindProc("GetAsyncKeyState")
)

func IsDown(vk int) bool {
	r, _, _ := syscall.SyscallN(procGetAsyncKeyState.Addr(), uintptr(vk))
	return r&0x8000 != 0
}

type Tracker struct {
	prev map[int]bool
}

func NewTracker() *Tracker {
	return &Tracker{prev: make(map[int]bool)}
}

func (t *Tracker) Pressed(vk int) bool {
	curr := IsDown(vk)
	prev := t.prev[vk]
	t.prev[vk] = curr
	return curr && !prev
}

func (t *Tracker) Released(vk int) bool {
	curr := IsDown(vk)
	prev := t.prev[vk]
	t.prev[vk] = curr
	return !curr && prev
}
