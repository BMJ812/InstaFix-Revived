package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestEmbedStatelessBrowserRedirectIsNeverEdgeCacheable(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "https://stateless.instagram7.test/reel/ABCDEFGHIJK/", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("postID", "ABCDEFGHIJK")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	EmbedStateless(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	if got := rec.Header().Get("Location"); got != "https://www.instagram.com/reel/ABCDEFGHIJK/" {
		t.Fatalf("Location = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := rec.Header().Get("CDN-Cache-Control"); got != "no-store" {
		t.Fatalf("CDN-Cache-Control = %q", got)
	}
	if got := rec.Header().Get("Cloudflare-CDN-Cache-Control"); got != "no-store" {
		t.Fatalf("Cloudflare-CDN-Cache-Control = %q", got)
	}
	if got := rec.Header().Get("Vary"); !strings.Contains(strings.ToLower(got), "user-agent") {
		t.Fatalf("Vary = %q, want User-Agent", got)
	}
}
