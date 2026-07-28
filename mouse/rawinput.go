package mouse

import (
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	RIDEV_INPUTSINK = 0x00000100
	RIDEV_REMOVE    = 0x00000001
	RID_INPUT       = 0x10000003
	RIM_TYPEMOUSE   = 0

	WM_APP_QUIT = 0x8001

	WM_DESTROY = 0x0002
	WM_INPUT   = 0x00FF

	CS_HREDRAW    = 0x0002
	CS_VREDRAW    = 0x0001
	HWND_MESSAGE  = ^uintptr(2)
	WS_OVERLAPPED = 0x00000000
	CW_USEDEFAULT = 0x80000000
	WS_EX_NOTHING = 0
)

type rawInputDevice struct {
	usUsagePage uint16
	usUsage     uint16
	dwFlags     uint32
	hwndTarget  syscall.Handle
}

type rawInputHeader struct {
	dwType  uint32
	dwSize  uint32
	hDevice syscall.Handle
	wParam  uintptr
}

type rawMouse struct {
	usFlags            uint16
	_                  uint16
	ulButtons          uint32
	ulRawButtons       uint32
	lLastX             int32
	lLastY             int32
	ulExtraInformation uint32
}

type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     syscall.Handle
	hIcon         syscall.Handle
	hCursor       syscall.Handle
	hbrBackground syscall.Handle
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       syscall.Handle
}

var (
	kernel32_dll = syscall.MustLoadDLL("kernel32.dll")
	user32_dll   = syscall.MustLoadDLL("user32.dll")

	procGetCurrentThreadId = kernel32_dll.MustFindProc("GetCurrentThreadId")

	procRegisterClassEx    = user32_dll.MustFindProc("RegisterClassExW")
	procCreateWindowEx     = user32_dll.MustFindProc("CreateWindowExW")
	procDefWindowProc      = user32_dll.MustFindProc("DefWindowProcW")
	procGetMessage         = user32_dll.MustFindProc("GetMessageW")
	procDispatchMessage    = user32_dll.MustFindProc("DispatchMessageW")
	procRegisterRawDevices = user32_dll.MustFindProc("RegisterRawInputDevices")
	procGetRawInputData    = user32_dll.MustFindProc("GetRawInputData")
	procDestroyWindow      = user32_dll.MustFindProc("DestroyWindow")
	procPostQuitMessage    = user32_dll.MustFindProc("PostQuitMessage")
	procPostThreadMessage  = user32_dll.MustFindProc("PostThreadMessageW")
)

var activeTracker *RawInputTracker

type RawInputTracker struct {
	lastUserInput time.Time
	mu            sync.Mutex
	hwnd          syscall.Handle
	threadId      uint32
	done          chan struct{}
}

func StartRawInput() (*RawInputTracker, error) {
	t := &RawInputTracker{done: make(chan struct{})}
	errCh := make(chan error, 1)

	go t.messagePump(errCh)

	select {
	case err := <-errCh:
		if err != nil {
			return nil, err
		}
		return t, nil
	case <-time.After(5 * time.Second):
		return nil, syscall.Errno(syscall.WAIT_TIMEOUT)
	}
}

func (t *RawInputTracker) messagePump(errCh chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(t.done)

	tid, _, _ := syscall.SyscallN(procGetCurrentThreadId.Addr())
	t.threadId = uint32(tid)

	hinst, _, _ := syscall.SyscallN(kernel32_dll.MustFindProc("GetModuleHandleW").Addr(), 0)
	if hinst == 0 {
		errCh <- syscall.GetLastError()
		return
	}

	wndProcCB := syscall.NewCallback(windowProc)

	className, _ := syscall.UTF16PtrFromString("GoCVStreamerRawInput")
	wc := wndClassExW{
		cbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
		style:         CS_HREDRAW | CS_VREDRAW,
		lpfnWndProc:   wndProcCB,
		hInstance:     syscall.Handle(hinst),
		lpszClassName: className,
	}

	ret, _, err := syscall.SyscallN(procRegisterClassEx.Addr(), uintptr(unsafe.Pointer(&wc)))
	if ret == 0 {
		errCh <- err
		return
	}

	hwnd, _, err := syscall.SyscallN(procCreateWindowEx.Addr(),
		WS_EX_NOTHING,
		uintptr(unsafe.Pointer(className)),
		0,
		WS_OVERLAPPED,
		CW_USEDEFAULT, CW_USEDEFAULT,
		CW_USEDEFAULT, CW_USEDEFAULT,
		uintptr(HWND_MESSAGE),
		0,
		hinst,
		0,
	)
	if hwnd == 0 {
		errCh <- err
		return
	}
	t.hwnd = syscall.Handle(hwnd)

	rid := rawInputDevice{
		usUsagePage: 0x01,
		usUsage:     0x02,
		dwFlags:     RIDEV_INPUTSINK,
		hwndTarget:  syscall.Handle(hwnd),
	}

	ret, _, err = syscall.SyscallN(procRegisterRawDevices.Addr(),
		uintptr(unsafe.Pointer(&rid)), 1, unsafe.Sizeof(rid))
	if ret == 0 {
		errCh <- err
		syscall.SyscallN(procDestroyWindow.Addr(), hwnd)
		return
	}

	activeTracker = t
	errCh <- nil

	var buf [48]byte

	for {
		ret, _, _ := syscall.SyscallN(procGetMessage.Addr(), uintptr(unsafe.Pointer(&buf[0])), 0, 0, 0)
		if ret == 0 || ret == ^uintptr(0) {
			return
		}

		msg := (*uint32)(unsafe.Pointer(&buf[8]))
		if *msg == WM_APP_QUIT {
			syscall.SyscallN(procDestroyWindow.Addr(), uintptr(t.hwnd))
			t.hwnd = 0
			continue
		}
		if *msg == WM_INPUT {
			t.processRawInput(*(*uintptr)(unsafe.Pointer(&buf[24])))
			continue
		}

		syscall.SyscallN(procDispatchMessage.Addr(), uintptr(unsafe.Pointer(&buf[0])))
	}
}

func (t *RawInputTracker) processRawInput(lParam uintptr) {
	var rbuf [256]byte
	var size uint32
	syscall.SyscallN(procGetRawInputData.Addr(),
		lParam, RID_INPUT, 0, uintptr(unsafe.Pointer(&size)), unsafe.Sizeof(rawInputHeader{}))

	if size == 0 || size > 256 {
		return
	}

	got, _, _ := syscall.SyscallN(procGetRawInputData.Addr(),
		lParam, RID_INPUT, uintptr(unsafe.Pointer(&rbuf)), uintptr(unsafe.Pointer(&size)), unsafe.Sizeof(rawInputHeader{}))

	if got != uintptr(size) {
		return
	}

	header := (*rawInputHeader)(unsafe.Pointer(&rbuf))
	if header.dwType != RIM_TYPEMOUSE {
		return
	}

	mouse := (*rawMouse)(unsafe.Add(unsafe.Pointer(&rbuf), unsafe.Sizeof(rawInputHeader{})))

	if mouse.ulExtraInformation == uint32(OurMouseExtraInfo) {
		return
	}

	if mouse.lLastX != 0 || mouse.lLastY != 0 {
		t.mu.Lock()
		t.lastUserInput = time.Now()
		t.mu.Unlock()
	}
}

func windowProc(hwnd syscall.Handle, msg uint32, wParam uintptr, lParam uintptr) uintptr {
	if msg == WM_INPUT {
		if t := activeTracker; t != nil {
			t.processRawInput(lParam)
		}
		return 0
	}

	if msg == WM_DESTROY {
		syscall.SyscallN(procPostQuitMessage.Addr(), 0)
		return 0
	}

	r, _, _ := syscall.SyscallN(procDefWindowProc.Addr(), uintptr(hwnd), uintptr(msg), wParam, lParam)
	return r
}

func (t *RawInputTracker) Stop() {
	if t.threadId != 0 {
		syscall.SyscallN(procPostThreadMessage.Addr(), uintptr(t.threadId), WM_APP_QUIT, 0, 0)
	}
	<-t.done
	activeTracker = nil
}

func (t *RawInputTracker) IsMoving(since time.Duration) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return time.Since(t.lastUserInput) < since
}

func IsMoving(since time.Duration) bool {
	if t := activeTracker; t != nil {
		return t.IsMoving(since)
	}
	return false
}
