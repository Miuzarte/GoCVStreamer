package cuda

import (
	"fmt"
	"sync"
	"syscall"
	"unsafe"

	"github.com/ebitengine/purego"
)

type CUresult int32

const CUDA_SUCCESS CUresult = 0

const (
	CU_CTX_SCHED_AUTO          = 0x00
	CU_CTX_SCHED_SPIN          = 0x01
	CU_CTX_SCHED_YIELD         = 0x02
	CU_CTX_SCHED_BLOCKING_SYNC = 0x04
)

const (
	CIG_DATA_TYPE_D3D12_COMMAND_QUEUE = 0
)

type CUctxCigParam struct {
	SharedDataType uint32
	_              [4]byte
	SharedData     unsafe.Pointer
}

type CUctxCreateParams struct {
	ExecAffinityParams    unsafe.Pointer
	NumExecAffinityParams int32
	_                     [4]byte
	CigParams             *CUctxCigParam
}

var (
	nvOnce sync.Once
	nvErr  error

	procCuInit                     func(flags uint32) int32
	procCuDeviceGet                func(device *int32, ordinal int32) int32
	procCuDeviceGetAttribute       func(pi *int32, attrib int32, dev int32) int32
	procCuCtxCreate                func(pctx *uintptr, params unsafe.Pointer, flags uint32, dev int32) int32
	procCuDevicePrimaryCtxRetain   func(pctx *uintptr, dev int32) int32
	procCuDevicePrimaryCtxSetFlags func(dev int32, flags uint32) int32
	procCuCtxPushCurrent           func(ctx uintptr) int32
	procCuCtxPopCurrent            func(pctx *uintptr) int32
	procCuCtxDestroy               func(ctx uintptr) int32
	procCuCtxGetCurrent            func(pctx *uintptr) int32
)

func ensureNvInit() error {
	nvOnce.Do(func() {
		h, err := syscall.LoadLibrary("nvcuda.dll")
		if err != nil {
			nvErr = fmt.Errorf("load nvcuda.dll: %w", err)
			return
		}
		purego.RegisterLibFunc(&procCuInit, uintptr(h), "cuInit")
		purego.RegisterLibFunc(&procCuDeviceGet, uintptr(h), "cuDeviceGet")
		purego.RegisterLibFunc(&procCuDeviceGetAttribute, uintptr(h), "cuDeviceGetAttribute")
		purego.RegisterLibFunc(&procCuCtxCreate, uintptr(h), "cuCtxCreate")
		purego.RegisterLibFunc(&procCuDevicePrimaryCtxRetain, uintptr(h), "cuDevicePrimaryCtxRetain")
		purego.RegisterLibFunc(&procCuDevicePrimaryCtxSetFlags, uintptr(h), "cuDevicePrimaryCtxSetFlags")
		purego.RegisterLibFunc(&procCuCtxPushCurrent, uintptr(h), "cuCtxPushCurrent")
		purego.RegisterLibFunc(&procCuCtxPopCurrent, uintptr(h), "cuCtxPopCurrent")
		purego.RegisterLibFunc(&procCuCtxDestroy, uintptr(h), "cuCtxDestroy")
		purego.RegisterLibFunc(&procCuCtxGetCurrent, uintptr(h), "cuCtxGetCurrent")
	})
	return nvErr
}

func initCUDA() error {
	if err := ensureNvInit(); err != nil {
		return err
	}
	if r := procCuInit(0); CUresult(r) != CUDA_SUCCESS {
		return fmt.Errorf("cuInit: %d", r)
	}
	return nil
}

func getDevice() (int32, error) {
	var dev int32
	if r := procCuDeviceGet(&dev, 0); CUresult(r) != CUDA_SUCCESS {
		return 0, fmt.Errorf("cuDeviceGet: %d", r)
	}
	return dev, nil
}

func createContext(flags uint32, cigParams *CUctxCigParam, dev int32) (uintptr, error) {
	var ctx uintptr
	var params unsafe.Pointer
	if cigParams != nil {
		cp := CUctxCreateParams{CigParams: cigParams}
		params = unsafe.Pointer(&cp)
	}
	if r := procCuCtxCreate(&ctx, params, flags, dev); CUresult(r) != CUDA_SUCCESS {
		return 0, fmt.Errorf("cuCtxCreate: %d", r)
	}
	return ctx, nil
}

func retainPrimaryContext(dev int32) (uintptr, error) {
	var ctx uintptr
	if r := procCuDevicePrimaryCtxRetain(&ctx, dev); CUresult(r) != CUDA_SUCCESS {
		return 0, fmt.Errorf("cuDevicePrimaryCtxRetain: %d", r)
	}
	return ctx, nil
}

func setPrimaryContextFlags(dev int32, flags uint32) error {
	if r := procCuDevicePrimaryCtxSetFlags(dev, flags); CUresult(r) != CUDA_SUCCESS {
		return fmt.Errorf("cuDevicePrimaryCtxSetFlags: %d", r)
	}
	return nil
}

func pushCurrent(ctx uintptr) error {
	if r := procCuCtxPushCurrent(ctx); CUresult(r) != CUDA_SUCCESS {
		return fmt.Errorf("cuCtxPushCurrent: %d", r)
	}
	return nil
}

func destroyContext(ctx uintptr) error {
	if r := procCuCtxDestroy(ctx); CUresult(r) != CUDA_SUCCESS {
		return fmt.Errorf("cuCtxDestroy: %d", r)
	}
	return nil
}

func getCurrentContext() (uintptr, error) {
	var ctx uintptr
	if r := procCuCtxGetCurrent(&ctx); CUresult(r) != CUDA_SUCCESS {
		return 0, fmt.Errorf("cuCtxGetCurrent: %d", r)
	}
	return ctx, nil
}
