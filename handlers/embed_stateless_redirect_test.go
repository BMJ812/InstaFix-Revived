package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRedirectStatelessNoStore(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "https://stateless.instagram7.com/reel/ABCDEFGHIJK/?direct=true", nil)
	rr := httptest.NewRecorder()
	target := "https://scontent.cdninstagram.com/video.mp4"

	redirectStatelessNoStore(rr, req, target)

	res := rr.Result()
	defer res.Body.Close()
	if res.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusFound)
	}
	if got := res.Header.Get("Location"); got != target {
		t.Fatalf("Location = %q, want %q", got, target)
	}
	for _, header := range []string{"Cache-Control", "CDN-Cache-Control", "Cloudflare-CDN-Cache-Control"} {
		if got := res.Header.Get(header); got == "" || got == "public" {
			t.Fatalf("%s must disable caching, got %q", header, got)
		}
	}
}
