#ifndef WGC_HELPER_H
#define WGC_HELPER_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

#if defined(_WIN32) && defined(WGC_BUILD)
#define WGC_API __declspec(dllexport)
#else
#define WGC_API
#endif

#define WGC_OK          0
#define WGC_NO_FRAME    1
#define WGC_ERROR       2
#define WGC_SIZE_CHANGED 3
#define WGC_CLOSED      4

typedef struct WgcPerfStats {
	int64_t system_qpc;     /* frame.SystemRelativeTime converted to QPC ticks */
	int64_t arrived_qpc;    /* FrameArrived callback entry (QPC) */
	int64_t ready_qpc;      /* frame data ready after Unmap (QPC) */
	int64_t tryget_us;      /* TryGetNextFrame duration */
	int64_t copy_us;        /* CopyResource duration */
	int64_t map_us;         /* Map duration */
	int64_t rowcopy_us;     /* row copy duration */
	uint64_t frame_id;      /* last processed frame id */
	uint64_t frames_received; /* frames processed by the callback */
	uint64_t frames_returned; /* frames returned to Go via GetImage */
	uint64_t frames_missed;   /* TryGetNextFrame returned null */
	int64_t wait_us;        /* GetImage event wait duration */
	int64_t copyout_us;     /* GetImage memcpy into the Go buffer */
} WgcPerfStats;

WGC_API int wgc_supported(void);
WGC_API int wgc_last_error(void);
WGC_API int32_t wgc_last_hresult(void);
WGC_API void wgc_set_borderless(int enabled);
WGC_API void *wgc_create_monitor(int display_index);
WGC_API void *wgc_create_window(uintptr_t hwnd, int client_area);
WGC_API uint32_t wgc_get_width(void *handle);
WGC_API uint32_t wgc_get_height(void *handle);
WGC_API int wgc_get_image(void *handle, uint8_t *dst, uint32_t dst_size, uint32_t timeout_ms);
WGC_API int wgc_get_stats(void *handle, WgcPerfStats *stats);
WGC_API void wgc_close(void *handle);

#ifdef __cplusplus
}
#endif

#endif
