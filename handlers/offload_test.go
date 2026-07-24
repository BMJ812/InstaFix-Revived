package handlers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	scraper "instafix/handlers/scraper"

	"github.com/go-chi/chi/v5"
)

func offloadRequest(t *testing.T, method, target, postID, mediaNum string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	route := chi.NewRouteContext()
	route.URLParams.Add("postID", postID)
	route.URLParams.Add("mediaNum", mediaNum)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
}

func stubOffloadData(t *testing.T, item *scraper.InstaData, refresh func(string) (*scraper.InstaData, error)) {
	t.Helper()
	oldGet := offloadGetDataPreferVideo
	oldRefresh := offloadRefreshDataPreferVideo
	oldClient := offloadVideoClient
	oldAllowed := offloadMediaURLAllowed
	offloadGetDataPreferVideo = func(string) (*scraper.InstaData, error) { return item, nil }
	offloadRefreshDataPreferVideo = refresh
	offloadVideoClient = &http.Client{}
	offloadMediaURLAllowed = func(string) bool { return true }
	t.Cleanup(func() {
		offloadGetDataPreferVideo = oldGet
		offloadRefreshDataPreferVideo = oldRefresh
		offloadVideoClient = oldClient
		offloadMediaURLAllowed = oldAllowed
	})
}

func TestOffloadVideoRangeReturnsDirect206(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Range"); got != "bytes=0-3" {
			t.Fatalf("upstream Range = %q", got)
		}
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Range", "bytes 0-3/10")
		w.Header().Set("Content-Length", "4")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "0123")
	}))
	defer upstream.Close()

	item := &scraper.InstaData{PostID: "DaRange1", Medias: []scraper.Media{{TypeName: "GraphVideo", URL: upstream.URL + "/video.mp4"}}}
	stubOffloadData(t, item, func(string) (*scraper.InstaData, error) {
		t.Fatal("refresh should not be called")
		return nil, nil
	})

	req := offloadRequest(t, http.MethodGet, "https://fix.example/offload/DaRange1/1", "DaRange1", "1")
	req.Header.Set("Range", "bytes=0-3")
	rec := httptest.NewRecorder()
	Offload(rec, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "" {
		t.Fatalf("video stream must not redirect, Location = %q", got)
	}
	if got := rec.Header().Get("Content-Range"); got != "bytes 0-3/10" {
		t.Fatalf("Content-Range = %q", got)
	}
	if got := rec.Body.String(); got != "0123" {
		t.Fatalf("body = %q", got)
	}
}

func TestOffloadVideoHEADFallsBackToRangeProbe(t *testing.T) {
	var headCalls atomic.Int32
	var getCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			headCalls.Add(1)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		getCalls.Add(1)
		if got := r.Header.Get("Range"); got != "bytes=0-0" {
			t.Fatalf("probe Range = %q", got)
		}
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Range", "bytes 0-0/100")
		w.Header().Set("Content-Length", "1")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "x")
	}))
	defer upstream.Close()

	item := &scraper.InstaData{PostID: "DaHead01", Medias: []scraper.Media{{TypeName: "GraphVideo", URL: upstream.URL + "/video.mp4"}}}
	stubOffloadData(t, item, func(string) (*scraper.InstaData, error) {
		t.Fatal("refresh should not be called")
		return nil, nil
	})

	rec := httptest.NewRecorder()
	Offload(rec, offloadRequest(t, http.MethodHead, "https://fix.example/offload/DaHead01/1", "DaHead01", "1"))

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("HEAD returned %d body bytes", rec.Body.Len())
	}
	if headCalls.Load() != 1 || getCalls.Load() != 1 {
		t.Fatalf("calls: HEAD=%d GET=%d", headCalls.Load(), getCalls.Load())
	}
}

func TestOffloadVideoRefreshesRejectedSignedURL(t *testing.T) {
	stale := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer stale.Close()
	fresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Range"); got != "bytes=0-2" {
			t.Fatalf("fresh Range = %q", got)
		}
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Range", "bytes 0-2/3")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "new")
	}))
	defer fresh.Close()

	staleItem := &scraper.InstaData{PostID: "DaStale1", Medias: []scraper.Media{{TypeName: "GraphVideo", URL: stale.URL + "/expired.mp4"}}}
	freshItem := &scraper.InstaData{PostID: "DaStale1", Medias: []scraper.Media{{TypeName: "GraphVideo", URL: fresh.URL + "/fresh.mp4"}}}
	var refreshCalls atomic.Int32
	stubOffloadData(t, staleItem, func(string) (*scraper.InstaData, error) {
		refreshCalls.Add(1)
		return freshItem, nil
	})

	req := offloadRequest(t, http.MethodGet, "https://fix.example/offload/DaStale1/1", "DaStale1", "1")
	req.Header.Set("Range", "bytes=0-2")
	rec := httptest.NewRecorder()
	Offload(rec, req)

	if rec.Code != http.StatusPartialContent || rec.Body.String() != "new" {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if refreshCalls.Load() != 1 {
		t.Fatalf("refresh calls = %d", refreshCalls.Load())
	}
	if rec.Header().Get("Location") != "" {
		t.Fatal("refreshed video response must not redirect")
	}
}

func TestOffloadImageStillRedirects(t *testing.T) {
	item := &scraper.InstaData{PostID: "DaImage1", Medias: []scraper.Media{{TypeName: "GraphImage", URL: "https://scontent.cdninstagram.com/image.jpg"}}}
	stubOffloadData(t, item, func(string) (*scraper.InstaData, error) { return nil, nil })

	rec := httptest.NewRecorder()
	Offload(rec, offloadRequest(t, http.MethodGet, "https://fix.example/offload/DaImage1/1", "DaImage1", "1"))

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Location"); !strings.Contains(got, "image.jpg") {
		t.Fatalf("Location = %q", got)
	}
}

func TestLegacyVideosRouteAlsoStreamsWithoutRedirect(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "video")
	}))
	defer upstream.Close()

	item := &scraper.InstaData{PostID: "DaLegacy", Medias: []scraper.Media{{TypeName: "GraphVideo", URL: upstream.URL + "/video.mp4"}}}
	stubOffloadData(t, item, func(string) (*scraper.InstaData, error) {
		t.Fatal("refresh should not be called")
		return nil, nil
	})

	rec := httptest.NewRecorder()
	Videos(rec, offloadRequest(t, http.MethodGet, "https://fix.example/videos/DaLegacy/1", "DaLegacy", "1"))

	if rec.Code != http.StatusOK || rec.Body.String() != "video" {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Location") != "" {
		t.Fatal("legacy video route must not redirect")
	}
}
