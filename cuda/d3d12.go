package cuda

import (
	"fmt"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type GUID struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

var (
	IID_ID3D12Device       = GUID{0x189819F1, 0x1DB6, 0x4B57, [8]byte{0xBE, 0x54, 0x18, 0x21, 0x33, 0x9B, 0x85, 0xF7}}
	IID_ID3D12CommandQueue = GUID{0x0EC870A6, 0x5D7E, 0x4C22, [8]byte{0x8C, 0xFC, 0x5B, 0xAA, 0xE0, 0x76, 0x16, 0xED}}
)

const (
	D3D12_COMMAND_LIST_TYPE_COMPUTE   = 2
	D3D12_COMMAND_QUEUE_PRIORITY_HIGH = 100
	D3D12_COMMAND_QUEUE_FLAG_NONE     = 0
)

type D3D12_COMMAND_QUEUE_DESC struct {
	Type     int32
	Priority int32
	Flags    int32
	NodeMask uint32
}

type ID3D12Device struct {
	vtbl *ID3D12DeviceVtbl
}

type ID3D12DeviceVtbl struct {
	QueryInterface          uintptr
	AddRef                  uintptr
	Release                 uintptr
	GetPrivateData          uintptr
	SetPrivateData          uintptr
	SetPrivateDataInterface uintptr
	SetName                 uintptr
	GetNodeCount            uintptr
	CreateCommandQueue      uintptr
}

type ID3D12CommandQueue struct {
	vtbl *ID3D12CommandQueueVtbl
}

type ID3D12CommandQueueVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
}

var (
	d3d12Once             sync.Once
	d3d12Dll              *windows.LazyDLL
	procD3D12CreateDevice *windows.LazyProc
	d3d12Err              error
)

func ensureD3D12Init() error {
	d3d12Once.Do(func() {
		d3d12Dll = windows.NewLazySystemDLL("d3d12.dll")
		if err := d3d12Dll.Load(); err != nil {
			d3d12Err = fmt.Errorf("load d3d12.dll: %w", err)
			return
		}
		procD3D12CreateDevice = d3d12Dll.NewProc("D3D12CreateDevice")
	})
	return d3d12Err
}

func createD3D12ComputeQueue() (unsafe.Pointer, error) {
	if err := ensureD3D12Init(); err != nil {
		return nil, err
	}

	var ppDevice unsafe.Pointer
	ret, _, _ := syscall.SyscallN(
		procD3D12CreateDevice.Addr(),
		uintptr(0),
		uintptr(0xb000),
		uintptr(unsafe.Pointer(&IID_ID3D12Device)),
		uintptr(unsafe.Pointer(&ppDevice)),
	)
	if ret != 0 {
		return nil, fmt.Errorf("D3D12CreateDevice: HRESULT 0x%X", ret)
	}
	if ppDevice == nil {
		return nil, fmt.Errorf("D3D12CreateDevice returned null device")
	}

	device := (*ID3D12Device)(ppDevice)

	desc := D3D12_COMMAND_QUEUE_DESC{
		Type:     D3D12_COMMAND_LIST_TYPE_COMPUTE,
		Priority: D3D12_COMMAND_QUEUE_PRIORITY_HIGH,
		Flags:    D3D12_COMMAND_QUEUE_FLAG_NONE,
		NodeMask: 0,
	}

	var ppQueue unsafe.Pointer
	ret, _, _ = syscall.SyscallN(
		device.vtbl.CreateCommandQueue,
		uintptr(unsafe.Pointer(device)),
		uintptr(unsafe.Pointer(&desc)),
		uintptr(unsafe.Pointer(&IID_ID3D12CommandQueue)),
		uintptr(unsafe.Pointer(&ppQueue)),
	)

	syscall.SyscallN(device.vtbl.Release, uintptr(unsafe.Pointer(device)))

	if ret != 0 {
		return nil, fmt.Errorf("CreateCommandQueue: HRESULT 0x%X", ret)
	}
	if ppQueue == nil {
		return nil, fmt.Errorf("CreateCommandQueue returned null queue")
	}

	return ppQueue, nil
}
