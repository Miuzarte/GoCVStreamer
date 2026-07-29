package cuda

import (
	"github.com/Miuzarte/GoCVStreamer/logger"
)

var log = logger.New("CUDA")

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

	if err := pushCurrent(ctx); err != nil {
		destroyContext(ctx)
		return 0, err
	}

	log.Info().
		Msg("CUDA primary context with SCHED_SPIN")
	return ctx, nil
}

func DestroyCurrentContext() {
	ctx, err := getCurrentContext()
	if err != nil {
		return
	}
	destroyContext(ctx)
	log.Debug().
		Msg("CUDA context destroyed")
}
