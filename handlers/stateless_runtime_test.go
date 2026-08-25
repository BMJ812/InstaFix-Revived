package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStatelessRuntimeEnabledAcceptsAzureAndLegacyAlias(t *testing.T) {
	for _, mode := range []string{"stateless", "stateless_azure", "stateless_cloudrun", "STATELESS_AZURE"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("INSTAFIX_EXPERIMENT_MODE", mode)
			if !statelessRuntimeEnabled() {
				t.Fatalf("expected stateless runtime for %q", mode)
			}
		})
	}
}

func TestLegacyVideoRouteIsHardDisabledInStatelessRuntime(t *testing.T) {
	t.Setenv("INSTAFIX_EXPERIMENT_MODE", "stateless_cloudrun")
	req := httptest.NewRequest(http.MethodGet, "https://stateless.instagram7.test/videos/DbLegacy1234/1", nil)
	rec := httptest.NewRecorder()
	Videos(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if got := rec.Header().Get("X-Instagram7-Experiment"); got != "stateless-azure" {
		t.Fatalf("experiment header = %q", got)
	}
}

func TestLegacyOffloadRouteIsHardDisabledInStatelessRuntime(t *testing.T) {
	t.Setenv("INSTAFIX_EXPERIMENT_MODE", "stateless_cloudrun")
	req := httptest.NewRequest(http.MethodGet, "https://stateless.instagram7.test/offload/DbLegacy1234/1.mp4", nil)
	rec := httptest.NewRecorder()
	Offload(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestLegacyPlayerRouteIsHardDisabledInStatelessRuntime(t *testing.T) {
	t.Setenv("INSTAFIX_EXPERIMENT_MODE", "stateless_cloudrun")
	req := httptest.NewRequest(http.MethodGet, "https://stateless.instagram7.test/player/DbLegacy1234/1", nil)
	rec := httptest.NewRecorder()
	Player(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
