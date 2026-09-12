package cuda

import (
	"github.com/Miuzarte/GoCVStreamer/logger"
)

var log = logger.New("CUDA")

// ctxDevice InitContextCiG 保留 primary context 的设备号, -1 表示未持有
var ctxDevice int32 = -1

func InitContextCiG() (uintptr, error) {
	if err := initCUDA(); err != nil {
		log.Warn().
			Err(err).
			Msg("cuInit failed, trying without CUDA")
		return 0, err
	}

	dev, err := getDevice()
	if err != nil {
		log.Warn().
			Err(err).
			Msg("cuDeviceGet failed")
		return 0, err
	}

	if err := setPrimaryContextFlags(dev, CU_CTX_SCHED_SPIN); err != nil {
		log.Warn().
			Err(err).
			Msg("failed to set primary context flags")
	}

	ctx, err := retainPrimaryContext(dev)
	if err != nil {
		return 0, err
	}
	ctxDevice = dev

	if err := pushCurrent(ctx); err != nil {
		releasePrimaryContext(dev)
		ctxDevice = -1
		return 0, err
	}

	log.Info().
		Msg("CUDA primary context with SCHED_SPIN")
	return ctx, nil
}

// DestroyCurrentContext 释放 InitContextCiG 保留的 primary context
//
// 之前这里用 cuCtxGetCurrent + cuCtxDestroy: CUDA 的 current context 是 per-thread 的,
// Close 所在的线程未必是当初 push 的那个, 且 cuCtxDestroy 会连带销毁 ORT / TensorRT 正在
// 使用的 primary context, 之后 TRT 析构必然报错 (Myelin unload_cuda error 700)
func DestroyCurrentContext() {
	dev := ctxDevice
	if dev < 0 {
		return
	}
	ctxDevice = -1

	if err := releasePrimaryContext(dev); err != nil {
		log.Warn().
			Err(err).
			Msg("failed to release CUDA primary context")
		return
	}
	log.Debug().
		Msg("CUDA primary context released")
}
