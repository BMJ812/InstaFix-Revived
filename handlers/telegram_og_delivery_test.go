package handlers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	scraper "instafix/handlers/scraper"
)

func TestTelegramStableMP4UsesOGStyleMinimalStream(t *testing.T) {
	var upstreamMethod atomic.Value
	var upstreamRange atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamMethod.Store(r.Method)
		upstreamRange.Store(r.Header.Get("Range"))
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Length", "5")
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Range", "bytes 0-4/5")
		w.Header().Set("ETag", `"upstream-etag"`)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "video")
	}))
	defer upstream.Close()

	item := &scraper.InstaData{
		PostID: "DbAutoOG1",
		Medias: []scraper.Media{{TypeName: "GraphVideo", URL: upstream.URL + "/video.mp4"}},
	}
	stubOffloadData(t, item, func(string) (*scraper.InstaData, error) {
		t.Fatal("refresh should not be called")
		return nil, nil
	})

	req := offloadRequest(t, http.MethodGet, "https://instagram7.test/offload/DbAutoOG1/1.mp4", "DbAutoOG1", "1.mp4")
	req.Header.Set("User-Agent", "TelegramBot (like TwitterBot)")
	req.Header.Set("Range", "bytes=0-1023")
	rec := httptest.NewRecorder()
	Offload(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "video" {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if got := upstreamMethod.Load(); got != http.MethodGet {
		t.Fatalf("upstream method = %q, want GET", got)
	}
	if got := upstreamRange.Load(); got != "" {
		t.Fatalf("upstream Range = %q, want empty", got)
	}
	for _, header := range []string{"Content-Length", "Content-Range", "Accept-Ranges", "ETag", "Last-Modified"} {
		if got := rec.Header().Get(header); got != "" {
			t.Fatalf("%s = %q, want omitted", header, got)
		}
	}
	if got := rec.Header().Get("Content-Type"); got != "video/mp4" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=3600" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := rec.Header().Get("Cross-Origin-Resource-Policy"); got != "cross-origin" {
		t.Fatalf("Cross-Origin-Resource-Policy = %q", got)
	}
	if got := rec.Header().Get("X-Instagram7-Video-Stream"); got != "telegram-minimal" {
		t.Fatalf("stream mode = %q", got)
	}
}
