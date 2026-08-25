package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestServeFavicon(t *testing.T) {
	recorder := httptest.NewRecorder()
	serveFavicon(recorder, httptest.NewRequest(http.MethodGet, "/favicon.svg", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := recorder.Header().Get("Content-Type"); got != "image/svg+xml; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if !strings.Contains(recorder.Body.String(), "<svg") {
		t.Fatal("favicon response is not SVG")
	}
}

func TestServeDemoReelAssets(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		contentType string
		handler     http.HandlerFunc
		magic       string
	}{
		{name: "video", path: "/assets/demo/instagram7-test-reel.mp4", contentType: "video/mp4", handler: serveDemoReelVideo, magic: "ftyp"},
		{name: "poster", path: "/assets/demo/instagram7-test-reel-poster.webp", contentType: "image/webp", handler: serveDemoReelPoster, magic: "WEBP"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			test.handler(recorder, httptest.NewRequest(http.MethodGet, test.path, nil))

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
			}
			if got := recorder.Header().Get("Content-Type"); got != test.contentType {
				t.Fatalf("Content-Type = %q, want %q", got, test.contentType)
			}
			if !strings.Contains(recorder.Header().Get("Cache-Control"), "immutable") {
				t.Fatalf("Cache-Control = %q", recorder.Header().Get("Cache-Control"))
			}
			if !strings.Contains(recorder.Body.String(), test.magic) {
				t.Fatalf("response body is missing %q magic", test.magic)
			}
		})
	}
}

func TestServeDemoReelVideoSupportsRangeAndHead(t *testing.T) {
	rangeRequest := httptest.NewRequest(http.MethodGet, "/assets/demo/instagram7-test-reel.mp4", nil)
	rangeRequest.Header.Set("Range", "bytes=0-63")
	rangeRecorder := httptest.NewRecorder()
	serveDemoReelVideo(rangeRecorder, rangeRequest)
	if rangeRecorder.Code != http.StatusPartialContent {
		t.Fatalf("range status = %d, want %d", rangeRecorder.Code, http.StatusPartialContent)
	}
	if got := rangeRecorder.Header().Get("Content-Range"); got != "bytes 0-63/"+strconv.Itoa(len(demoReelMP4)) {
		t.Fatalf("Content-Range = %q", got)
	}
	if rangeRecorder.Body.Len() != 64 {
		t.Fatalf("range body length = %d, want 64", rangeRecorder.Body.Len())
	}

	headRecorder := httptest.NewRecorder()
	serveDemoReelVideo(headRecorder, httptest.NewRequest(http.MethodHead, "/assets/demo/instagram7-test-reel.mp4", nil))
	if headRecorder.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want %d", headRecorder.Code, http.StatusOK)
	}
	if body, err := io.ReadAll(headRecorder.Result().Body); err != nil || len(body) != 0 {
		t.Fatalf("HEAD body length = %d, err = %v", len(body), err)
	}
}
