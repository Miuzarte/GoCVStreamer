#define WGC_BUILD
#include "wgc_helper.h"

#ifndef WIN32_LEAN_AND_MEAN
#define WIN32_LEAN_AND_MEAN
#endif
#include <windows.h>

#include <d3d11.h>
#include <dxgi1_2.h>
#include <dwmapi.h>

#include <winrt/Windows.Foundation.h>
#include <winrt/Windows.Foundation.Metadata.h>
#include <winrt/Windows.Graphics.Capture.h>
#include <winrt/Windows.Graphics.DirectX.h>
#include <winrt/Windows.Graphics.DirectX.Direct3D11.h>
#include <windows.graphics.capture.interop.h>
#include <windows.graphics.directx.direct3d11.interop.h>

#include <atomic>
#include <cstring>
#include <mutex>
#include <vector>

#pragma comment(lib, "windowsapp")
#pragma comment(lib, "d3d11")
#pragma comment(lib, "dxgi")
#pragma comment(lib, "ole32")
#pragma comment(lib, "user32")
#pragma comment(lib, "dwmapi")

namespace wgc = winrt::Windows::Graphics::Capture;
namespace wgdx = winrt::Windows::Graphics::DirectX;
namespace wgdxd3d11 = winrt::Windows::Graphics::DirectX::Direct3D11;

static std::once_flag g_winrt_once;
static std::atomic<int> g_last_error{0};
static std::atomic<int32_t> g_last_hresult{0};
static std::atomic<bool> g_borderless{false};

enum WgcErrorStage {
	WGC_ERR_NONE = 0,
	WGC_ERR_UNSUPPORTED = 1,
	WGC_ERR_MONITORS = 2,
	WGC_ERR_EVENT = 3,
	WGC_ERR_D3D = 4,
	WGC_ERR_DXGI_QI = 5,
	WGC_ERR_WINRT_DEVICE = 6,
	WGC_ERR_CREATE_ITEM = 7,
	WGC_ERR_POOL = 8,
	WGC_ERR_START = 9,
	WGC_ERR_UNKNOWN = 99,
};

static void set_error(int stage, int32_t hr)
{
	g_last_error.store(stage, std::memory_order_release);
	g_last_hresult.store(hr, std::memory_order_release);
}

static int64_t qpc_frequency()
{
	static int64_t freq = []() {
		LARGE_INTEGER li{};
		QueryPerformanceFrequency(&li);
		return li.QuadPart;
	}();
	return freq;
}

static inline int64_t qpc_now()
{
	LARGE_INTEGER li{};
	QueryPerformanceCounter(&li);
	return li.QuadPart;
}

static inline int64_t qpc_diff_us(int64_t start, int64_t end)
{
	return (end - start) * 1000000LL / qpc_frequency();
}

extern "C" WGC_API int wgc_last_error(void)
{
	return g_last_error.load(std::memory_order_acquire);
}

extern "C" WGC_API int32_t wgc_last_hresult(void)
{
	return g_last_hresult.load(std::memory_order_acquire);
}

extern "C" WGC_API void wgc_set_borderless(int enabled)
{
	g_borderless.store(enabled != 0, std::memory_order_release);
}

static void ensure_winrt()
{
	std::call_once(g_winrt_once, []() {
		winrt::init_apartment(winrt::apartment_type::multi_threaded);
	});
}

extern "C" WGC_API int wgc_supported(void)
try {
	return winrt::Windows::Foundation::Metadata::ApiInformation::IsApiContractPresent(
		       L"Windows.Foundation.UniversalApiContract", 8)
		       ? 1
		       : 0;
} catch (...) {
	return 0;
}

static bool cursor_toggle_supported()
try {
	return winrt::Windows::Foundation::Metadata::ApiInformation::IsPropertyPresent(
		L"Windows.Graphics.Capture.GraphicsCaptureSession", L"IsCursorCaptureEnabled");
} catch (...) {
	return false;
}

static bool border_toggle_supported()
try {
	return winrt::Windows::Foundation::Metadata::ApiInformation::IsPropertyPresent(
		L"Windows.Graphics.Capture.GraphicsCaptureSession", L"IsBorderRequired");
} catch (...) {
	return false;
}

struct MonitorInfo {
	HMONITOR hMonitor;
	RECT rect;
};

static BOOL CALLBACK monitor_enum_proc(HMONITOR hMonitor, HDC, LPRECT lprcMonitor, LPARAM dwData)
{
	auto *monitors = reinterpret_cast<std::vector<MonitorInfo> *>(dwData);
	monitors->push_back({hMonitor, *lprcMonitor});
	return TRUE;
}

static std::vector<MonitorInfo> enumerate_monitors()
{
	std::vector<MonitorInfo> monitors;
	EnumDisplayMonitors(nullptr, nullptr, monitor_enum_proc, reinterpret_cast<LPARAM>(&monitors));
	return monitors;
}

struct WgcCapture {
	HWND window{nullptr};
	HMONITOR monitor{nullptr};
	BOOL client_area{FALSE};

	winrt::com_ptr<ID3D11Device> d3d_device;
	winrt::com_ptr<ID3D11DeviceContext> d3d_context;
	wgdxd3d11::IDirect3DDevice device{nullptr};
	wgc::GraphicsCaptureItem item{nullptr};
	wgc::Direct3D11CaptureFramePool frame_pool{nullptr};
	wgc::GraphicsCaptureSession session{nullptr};
	wgc::Direct3D11CaptureFramePool::FrameArrived_revoker frame_arrived_revoker;
	wgc::GraphicsCaptureItem::Closed_revoker closed_revoker;
	winrt::Windows::Graphics::SizeInt32 last_size{};

	winrt::com_ptr<ID3D11Texture2D> staging;
	uint32_t staging_w{0};
	uint32_t staging_h{0};

	std::mutex frame_mutex;
	std::vector<uint8_t> frame_data;
	uint32_t width{0};
	uint32_t height{0};
	std::atomic<uint64_t> frame_id{0};
	uint64_t last_returned{0};
	HANDLE frame_event{nullptr};

	std::atomic<bool> active{false};
	std::atomic<bool> shutting_down{false};

	WgcPerfStats last_stats{};
	uint64_t frames_received{0};
	uint64_t frames_returned{0};
	uint64_t frames_missed{0};

	void on_closed(wgc::GraphicsCaptureItem const &, winrt::Windows::Foundation::IInspectable const &)
	{
		active = false;
	}

	void on_frame_arrived(wgc::Direct3D11CaptureFramePool const &sender,
			      winrt::Windows::Foundation::IInspectable const &);
	void on_frame_arrived_impl(wgc::Direct3D11CaptureFramePool const &sender,
				   winrt::Windows::Foundation::IInspectable const &);
};

static void destroy_capture(WgcCapture *c)
{
	if (!c) {
		return;
	}
	c->shutting_down = true;
	try {
		c->frame_arrived_revoker.revoke();
	} catch (...) {
	}
	try {
		c->closed_revoker.revoke();
	} catch (...) {
	}
	try {
		if (c->session) {
			c->session.Close();
		}
	} catch (...) {
	}
	try {
		if (c->frame_pool) {
			c->frame_pool.Close();
		}
	} catch (...) {
	}
	if (c->frame_event) {
		CloseHandle(c->frame_event);
		c->frame_event = nullptr;
	}
	delete c;
}

static bool get_client_box(HWND window, uint32_t width, uint32_t height, D3D11_BOX *client_box)
{
	RECT client_rect{};
	RECT window_rect{};
	POINT upper_left{};

	bool ok = !IsIconic(window) && GetClientRect(window, &client_rect) && !IsIconic(window) &&
		  (client_rect.right > 0) && (client_rect.bottom > 0) &&
		  (DwmGetWindowAttribute(window, DWMWA_EXTENDED_FRAME_BOUNDS, &window_rect,
					 sizeof(window_rect)) == S_OK) &&
		  ClientToScreen(window, &upper_left);
	if (ok) {
		const uint32_t left = (upper_left.x > window_rect.left)
					      ? static_cast<uint32_t>(upper_left.x - window_rect.left)
					      : 0;
		const uint32_t top = (upper_left.y > window_rect.top)
					     ? static_cast<uint32_t>(upper_left.y - window_rect.top)
					     : 0;

		uint32_t tw = 1;
		if (width > left) {
			tw = (width - left < static_cast<uint32_t>(client_rect.right))
				     ? (width - left)
				     : static_cast<uint32_t>(client_rect.right);
		}
		uint32_t th = 1;
		if (height > top) {
			th = (height - top < static_cast<uint32_t>(client_rect.bottom))
				     ? (height - top)
				     : static_cast<uint32_t>(client_rect.bottom);
		}

		client_box->left = left;
		client_box->top = top;
		client_box->right = left + tw;
		client_box->bottom = top + th;
		client_box->front = 0;
		client_box->back = 1;

		ok = (client_box->right <= width) && (client_box->bottom <= height);
	}
	return ok;
}

void WgcCapture::on_frame_arrived(wgc::Direct3D11CaptureFramePool const &sender,
				  winrt::Windows::Foundation::IInspectable const &args)
try {
	on_frame_arrived_impl(sender, args);
} catch (...) {
	/* never let exceptions escape into the WinRT event dispatcher */
}

void WgcCapture::on_frame_arrived_impl(wgc::Direct3D11CaptureFramePool const &sender,
				       winrt::Windows::Foundation::IInspectable const &)
{
	if (shutting_down.load(std::memory_order_acquire)) {
		return;
	}

	const int64_t q0 = qpc_now();
	wgc::Direct3D11CaptureFrame frame{nullptr};
	try {
		frame = sender.TryGetNextFrame();
	} catch (...) {
		return;
	}
	const int64_t q1 = qpc_now();
	if (!frame) {
		std::lock_guard<std::mutex> lock(frame_mutex);
		frames_missed++;
		return;
	}

	winrt::com_ptr<ID3D11Texture2D> surface_tex;
	try {
		auto access = frame.Surface().as<::Windows::Graphics::DirectX::Direct3D11::IDirect3DDxgiInterfaceAccess>();
		winrt::check_hresult(access->GetInterface(winrt::guid_of<ID3D11Texture2D>(), surface_tex.put_void()));
	} catch (...) {
		return;
	}
	if (!surface_tex) {
		return;
	}
	const int64_t q2 = qpc_now();

	D3D11_TEXTURE2D_DESC desc{};
	surface_tex->GetDesc(&desc);
	if (desc.Format != DXGI_FORMAT_B8G8R8A8_UNORM) {
		return; /* v1: SDR only */
	}

	D3D11_BOX box{};
	uint32_t out_w = desc.Width;
	uint32_t out_h = desc.Height;
	bool have_frame = true;
	if (client_area && window) {
		D3D11_BOX cb{};
		if (get_client_box(window, desc.Width, desc.Height, &cb)) {
			box = cb;
			out_w = cb.right - cb.left;
			out_h = cb.bottom - cb.top;
		} else {
			have_frame = false; /* minimized / unavailable: keep last frame */
		}
	}
	if (!have_frame || out_w == 0 || out_h == 0) {
		return;
	}

	std::lock_guard<std::mutex> lock(frame_mutex);
	if (shutting_down.load(std::memory_order_acquire)) {
		return;
	}

	if (!staging || staging_w != desc.Width || staging_h != desc.Height) {
		D3D11_TEXTURE2D_DESC sd = desc;
		sd.Usage = D3D11_USAGE_STAGING;
		sd.BindFlags = 0;
		sd.CPUAccessFlags = D3D11_CPU_ACCESS_READ;
		sd.MiscFlags = 0;
		staging = nullptr;
		HRESULT hr = d3d_device->CreateTexture2D(&sd, nullptr, staging.put());
		if (FAILED(hr)) {
			return;
		}
		staging_w = desc.Width;
		staging_h = desc.Height;
	}

	d3d_context->CopyResource(staging.get(), surface_tex.get());
	const int64_t q3 = qpc_now();

	D3D11_MAPPED_SUBRESOURCE mapped{};
	HRESULT hr = d3d_context->Map(staging.get(), 0, D3D11_MAP_READ, 0, &mapped);
	if (FAILED(hr) || !mapped.pData) {
		return;
	}
	const int64_t q4 = qpc_now();

	const size_t frame_bytes = static_cast<size_t>(out_w) * out_h * 4;
	if (frame_data.size() != frame_bytes) {
		width = out_w;
		height = out_h;
		frame_data.resize(frame_bytes);
	}

	const uint8_t *src = static_cast<const uint8_t *>(mapped.pData);
	const size_t src_pitch = mapped.RowPitch;
	if (client_area && window) {
		for (uint32_t y = box.top; y < box.bottom; y++) {
			std::memcpy(frame_data.data() + static_cast<size_t>(y - box.top) * width * 4,
				    src + static_cast<size_t>(y) * src_pitch + static_cast<size_t>(box.left) * 4,
				    static_cast<size_t>(width) * 4);
		}
	} else {
		for (uint32_t y = 0; y < height; y++) {
			std::memcpy(frame_data.data() + static_cast<size_t>(y) * width * 4,
				    src + static_cast<size_t>(y) * src_pitch, static_cast<size_t>(width) * 4);
		}
	}
	const int64_t q5 = qpc_now();
	d3d_context->Unmap(staging.get(), 0);
	const int64_t q6 = qpc_now();

	auto content = frame.ContentSize();
	if (content.Width != last_size.Width || content.Height != last_size.Height) {
		last_size = content;
		try {
			frame_pool.Recreate(device, wgdx::DirectXPixelFormat::B8G8R8A8UIntNormalized, 2, content);
		} catch (...) {
			active = false;
		}
	}

	frame_id.fetch_add(1, std::memory_order_release);

	int64_t system_qpc = q0;
	try {
		/* count is in 100ns units; multiplying by the QPC frequency can
		   overflow int64 after ~25h of uptime, so convert via long double. */
		system_qpc = static_cast<int64_t>(static_cast<long double>(frame.SystemRelativeTime().count()) *
						  static_cast<long double>(qpc_frequency()) / 10000000.0L);
	} catch (...) {
		/* fall back to the callback entry time */
	}

	WgcPerfStats s{};
	s.system_qpc = system_qpc;
	s.arrived_qpc = q0;
	s.ready_qpc = q6;
	s.tryget_us = qpc_diff_us(q0, q1);
	s.copy_us = qpc_diff_us(q2, q3);
	s.map_us = qpc_diff_us(q3, q4);
	s.rowcopy_us = qpc_diff_us(q4, q5);
	s.frame_id = frame_id.load(std::memory_order_relaxed);
	s.frames_received = ++frames_received;
	s.frames_returned = frames_returned;
	s.frames_missed = frames_missed;
	last_stats = s;

	SetEvent(frame_event);
}

static WgcCapture *create_capture(HWND window, HMONITOR monitor, BOOL client_area)
{
	ensure_winrt();

	WgcCapture *c = nullptr;
	try {
		if (!window && !monitor) {
			set_error(WGC_ERR_UNKNOWN, E_INVALIDARG);
			return nullptr;
		}

		c = new WgcCapture();
		c->window = window;
		c->monitor = monitor;
		c->client_area = client_area;
		c->frame_event = CreateEventW(nullptr, FALSE, FALSE, nullptr);
		if (!c->frame_event) {
			set_error(WGC_ERR_EVENT, static_cast<int32_t>(GetLastError()));
			destroy_capture(c);
			return nullptr;
		}

		const UINT flags = D3D11_CREATE_DEVICE_BGRA_SUPPORT;
		const D3D_FEATURE_LEVEL levels[] = {D3D_FEATURE_LEVEL_11_0};
		HRESULT hr = D3D11CreateDevice(nullptr, D3D_DRIVER_TYPE_HARDWARE, nullptr, flags, levels, 1,
					       D3D11_SDK_VERSION, c->d3d_device.put(), nullptr, nullptr);
		if (FAILED(hr)) {
			hr = D3D11CreateDevice(nullptr, D3D_DRIVER_TYPE_WARP, nullptr, flags, levels, 1,
					       D3D11_SDK_VERSION, c->d3d_device.put(), nullptr, nullptr);
		}
		if (FAILED(hr) || !c->d3d_device) {
			set_error(WGC_ERR_D3D, static_cast<int32_t>(hr));
			destroy_capture(c);
			return nullptr;
		}

		winrt::com_ptr<IDXGIDevice> dxgi_device;
		hr = c->d3d_device->QueryInterface(winrt::guid_of<IDXGIDevice>(), dxgi_device.put_void());
		if (FAILED(hr)) {
			set_error(WGC_ERR_DXGI_QI, static_cast<int32_t>(hr));
			destroy_capture(c);
			return nullptr;
		}
		c->d3d_device->GetImmediateContext(c->d3d_context.put());

		winrt::com_ptr<::IInspectable> inspectable;
		hr = CreateDirect3D11DeviceFromDXGIDevice(dxgi_device.get(), inspectable.put());
		if (FAILED(hr)) {
			set_error(WGC_ERR_WINRT_DEVICE, static_cast<int32_t>(hr));
			destroy_capture(c);
			return nullptr;
		}
		c->device = inspectable.as<wgdxd3d11::IDirect3DDevice>();

		auto factory = winrt::get_activation_factory<wgc::GraphicsCaptureItem, IGraphicsCaptureItemInterop>();
		winrt::com_ptr<::IInspectable> item_interop;
		if (window) {
			hr = factory->CreateForWindow(window,
						       winrt::guid_of<ABI::Windows::Graphics::Capture::IGraphicsCaptureItem>(),
						       item_interop.put_void());
		} else {
			hr = factory->CreateForMonitor(monitor,
							winrt::guid_of<ABI::Windows::Graphics::Capture::IGraphicsCaptureItem>(),
							item_interop.put_void());
		}
		if (FAILED(hr)) {
			set_error(WGC_ERR_CREATE_ITEM, static_cast<int32_t>(hr));
			destroy_capture(c);
			return nullptr;
		}
		c->item = item_interop.as<wgc::GraphicsCaptureItem>();

		auto size = c->item.Size();
		if (size.Width <= 0 || size.Height <= 0) {
			RECT r{};
			if (window) {
				GetClientRect(window, &r);
			} else {
				MONITORINFO mi{};
				mi.cbSize = sizeof(mi);
				GetMonitorInfoW(monitor, &mi);
				r = mi.rcMonitor;
			}
			size.Width = r.right - r.left;
			size.Height = r.bottom - r.top;
		}
		c->last_size = size;
		/* expose the initial size before the first frame arrives */
		c->width = static_cast<uint32_t>(size.Width);
		c->height = static_cast<uint32_t>(size.Height);
		c->frame_data.resize(static_cast<size_t>(c->width) * c->height * 4);

		c->frame_pool = wgc::Direct3D11CaptureFramePool::CreateFreeThreaded(
			c->device, wgdx::DirectXPixelFormat::B8G8R8A8UIntNormalized, 2, size);
		c->session = c->frame_pool.CreateCaptureSession(c->item);

		if (cursor_toggle_supported()) {
			try {
				c->session.IsCursorCaptureEnabled(false);
			} catch (...) {
			}
		}

		if (g_borderless.load(std::memory_order_acquire) && border_toggle_supported()) {
			try {
				/* request borderless access, then hide the system yellow border (same flow as OBS) */
				auto access = wgc::GraphicsCaptureAccess::RequestAccessAsync(
					wgc::GraphicsCaptureAccessKind::Borderless);
				(void)access.get();
			} catch (...) {
			}
			try {
				c->session.IsBorderRequired(false);
			} catch (...) {
			}
		}

		c->closed_revoker = c->item.Closed(winrt::auto_revoke, {c, &WgcCapture::on_closed});
		c->frame_arrived_revoker =
			c->frame_pool.FrameArrived(winrt::auto_revoke, {c, &WgcCapture::on_frame_arrived});

		try {
			c->session.StartCapture();
		} catch (...) {
			set_error(WGC_ERR_START, static_cast<int32_t>(winrt::to_hresult()));
			destroy_capture(c);
			return nullptr;
		}
		c->active = true;
		set_error(WGC_ERR_NONE, 0);
		return c;
	} catch (...) {
		set_error(WGC_ERR_UNKNOWN, static_cast<int32_t>(winrt::to_hresult()));
		destroy_capture(c);
		return nullptr;
	}
}

extern "C" WGC_API void *wgc_create_monitor(int display_index)
{
	if (!wgc_supported()) {
		set_error(WGC_ERR_UNSUPPORTED, 0);
		return nullptr;
	}
	const auto monitors = enumerate_monitors();
	if (display_index < 0 || display_index >= static_cast<int>(monitors.size())) {
		set_error(WGC_ERR_MONITORS, 0);
		return nullptr;
	}
	return create_capture(nullptr, monitors[static_cast<size_t>(display_index)].hMonitor, FALSE);
}

extern "C" WGC_API void *wgc_create_window(uintptr_t hwnd, int client_area)
{
	if (!wgc_supported() || !hwnd) {
		set_error(WGC_ERR_UNSUPPORTED, 0);
		return nullptr;
	}
	return create_capture(reinterpret_cast<HWND>(hwnd), nullptr, client_area ? TRUE : FALSE);
}

extern "C" WGC_API uint32_t wgc_get_width(void *handle)
{
	auto *c = static_cast<WgcCapture *>(handle);
	if (!c) {
		return 0;
	}
	std::lock_guard<std::mutex> lock(c->frame_mutex);
	return c->width;
}

extern "C" WGC_API uint32_t wgc_get_height(void *handle)
{
	auto *c = static_cast<WgcCapture *>(handle);
	if (!c) {
		return 0;
	}
	std::lock_guard<std::mutex> lock(c->frame_mutex);
	return c->height;
}

extern "C" WGC_API int wgc_get_image(void *handle, uint8_t *dst, uint32_t dst_size, uint32_t timeout_ms)
{
	auto *c = static_cast<WgcCapture *>(handle);
	if (!c) {
		return WGC_ERROR;
	}
	if (!c->active.load(std::memory_order_acquire)) {
		return WGC_CLOSED;
	}

	int64_t wait_us = 0;
	uint64_t current = c->frame_id.load(std::memory_order_acquire);
	if (current == c->last_returned) {
		const int64_t w0 = qpc_now();
		if (WaitForSingleObject(c->frame_event, timeout_ms) != WAIT_OBJECT_0) {
			return WGC_NO_FRAME;
		}
		wait_us = qpc_diff_us(w0, qpc_now());
		current = c->frame_id.load(std::memory_order_acquire);
		if (current == c->last_returned) {
			return WGC_NO_FRAME;
		}
	}

	std::lock_guard<std::mutex> lock(c->frame_mutex);
	if (c->width == 0 || c->height == 0) {
		return WGC_NO_FRAME;
	}
	const size_t need = static_cast<size_t>(c->width) * c->height * 4;
	if (need != dst_size) {
		return WGC_SIZE_CHANGED;
	}
	int64_t copyout_us = 0;
	if (dst && !c->frame_data.empty()) {
		const int64_t c0 = qpc_now();
		std::memcpy(dst, c->frame_data.data(), need);
		copyout_us = qpc_diff_us(c0, qpc_now());
	}
	c->last_returned = current;
	c->frames_returned++;
	c->last_stats.wait_us = wait_us;
	c->last_stats.copyout_us = copyout_us;
	return WGC_OK;
}

extern "C" WGC_API int wgc_get_stats(void *handle, WgcPerfStats *stats)
{
	auto *c = static_cast<WgcCapture *>(handle);
	if (!c || !stats) {
		return 0;
	}
	std::lock_guard<std::mutex> lock(c->frame_mutex);
	*stats = c->last_stats;
	return 1;
}

extern "C" WGC_API void wgc_close(void *handle)
{
	destroy_capture(static_cast<WgcCapture *>(handle));
}
