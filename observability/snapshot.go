package observability

import (
	"runtime"
	"time"
)

// Snapshot is a small, non-sensitive runtime view intended for stateless
// experiment health checks. It deliberately contains only aggregate counters:
// no post IDs, signed media URLs, client IPs, captions, cookies, or tokens.
type Snapshot struct {
	UptimeSeconds uint64 `json:"uptime_seconds"`

	Requests      uint64 `json:"requests"`
	CacheHits     uint64 `json:"cache_hits"`
	ScrapeSuccess uint64 `json:"scrape_success"`
	ScrapeFailure uint64 `json:"scrape_failure"`

	PreviewRequests uint64 `json:"preview_requests"`
	PreviewFull     uint64 `json:"preview_full"`
	PreviewFallback uint64 `json:"preview_fallback"`
	PreviewFailed   uint64 `json:"preview_failed"`
	PreviewVideos   uint64 `json:"preview_videos"`
	PreviewImages   uint64 `json:"preview_images"`

	TelegramRequests uint64 `json:"telegram_requests"`
	TelegramFull     uint64 `json:"telegram_full"`
	TelegramFallback uint64 `json:"telegram_fallback"`
	TelegramFailed   uint64 `json:"telegram_failed"`

	MediaStreamHead     uint64 `json:"media_stream_head"`
	MediaStreamGet      uint64 `json:"media_stream_get"`
	MediaStreamRange    uint64 `json:"media_stream_range"`
	MediaStreamFailures uint64 `json:"media_stream_failures"`
	MediaStreamBytes    uint64 `json:"media_stream_bytes"`

	AuthUsed          uint64 `json:"auth_used"`
	OGProxyServed     uint64 `json:"og_proxy_served"`
	OGClientRedirects uint64 `json:"og_client_redirects"`

	RuntimeSysBytes   uint64 `json:"runtime_sys_bytes"`
	PeakRuntimeBytes  uint64 `json:"peak_runtime_bytes"`
	Goroutines        uint64 `json:"goroutines"`
	PeakGoroutines    uint64 `json:"peak_goroutines"`
	ActiveMediaStream int64  `json:"active_media_streams"`
	PeakMediaStreams  int64  `json:"peak_media_streams"`
}

func (m *Manager) Snapshot() Snapshot {
	if m == nil {
		return Snapshot{}
	}

	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	goroutines := uint64(runtime.NumGoroutine())
	updateAtomicMax(&m.peakRuntimeBytes, stats.Sys)
	updateAtomicMax(&m.peakGoroutines, goroutines)

	uptime := time.Since(m.started)
	if uptime < 0 {
		uptime = 0
	}
	return Snapshot{
		UptimeSeconds: uint64(uptime / time.Second),

		Requests:      m.requests.Load(),
		CacheHits:     m.cacheHits.Load(),
		ScrapeSuccess: m.scrapeSuccess.Load(),
		ScrapeFailure: m.scrapeFailure.Load(),

		PreviewRequests: m.previewRequests.Load(),
		PreviewFull:     m.previewFull.Load(),
		PreviewFallback: m.previewFallback.Load(),
		PreviewFailed:   m.previewFailed.Load(),
		PreviewVideos:   m.previewVideos.Load(),
		PreviewImages:   m.previewImages.Load(),

		TelegramRequests: m.previewTelegram.Load(),
		TelegramFull:     m.previewTelegramFull.Load(),
		TelegramFallback: m.previewTelegramFallback.Load(),
		TelegramFailed:   m.previewTelegramFailed.Load(),

		MediaStreamHead:     m.mediaStreamHead.Load(),
		MediaStreamGet:      m.mediaStreamGet.Load(),
		MediaStreamRange:    m.mediaStreamRange.Load(),
		MediaStreamFailures: m.mediaStreamFailures.Load(),
		MediaStreamBytes:    m.mediaStreamBytes.Load(),

		AuthUsed:          m.authUsed.Load(),
		OGProxyServed:     m.ogProxyServed.Load(),
		OGClientRedirects: m.ogClientRedirects.Load(),

		RuntimeSysBytes:   stats.Sys,
		PeakRuntimeBytes:  m.peakRuntimeBytes.Load(),
		Goroutines:        goroutines,
		PeakGoroutines:    m.peakGoroutines.Load(),
		ActiveMediaStream: m.activeMediaStreams.Load(),
		PeakMediaStreams:  m.peakMediaStreams.Load(),
	}
}
