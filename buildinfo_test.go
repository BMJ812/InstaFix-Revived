package main

import (
	"encoding/json"
	"instafix/observability"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthHandlerExposesAggregateMetricsOnlyForStatelessRuntime(t *testing.T) {
	old := observability.Default
	observability.Configure(observability.Config{})
	t.Cleanup(func() { observability.Default = old })

	t.Setenv("INSTAFIX_EXPERIMENT_MODE", "stateless_azure")
	t.Setenv("INSTAFIX_EXPERIMENT_LABEL", "stateless-azure")

	// A metadata-only stateless origin must begin with zero proxied media bytes.
	observability.Default.RecordPreview(
		httptest.NewRequest(http.MethodGet, "https://stateless.instagram7.test/reel/TestCode123/", nil),
		"TestCode123",
		"full",
		"video",
	)

	rec := httptest.NewRecorder()
	healthHandler(rec, httptest.NewRequest(http.MethodGet, "https://stateless.instagram7.test/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var payload struct {
		Stateless bool `json:"stateless"`
		Metrics   struct {
			PreviewRequests  uint64 `json:"preview_requests"`
			PreviewVideos    uint64 `json:"preview_videos"`
			MediaStreamBytes uint64 `json:"media_stream_bytes"`
			AuthUsed         uint64 `json:"auth_used"`
			OGProxyServed    uint64 `json:"og_proxy_served"`
		} `json:"metrics"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode health JSON: %v", err)
	}
	if !payload.Stateless {
		t.Fatal("expected stateless=true")
	}
	if payload.Metrics.PreviewRequests != 1 || payload.Metrics.PreviewVideos != 1 {
		t.Fatalf("preview metrics = %+v", payload.Metrics)
	}
	if payload.Metrics.MediaStreamBytes != 0 {
		t.Fatalf("media_stream_bytes = %d, want 0", payload.Metrics.MediaStreamBytes)
	}
	if payload.Metrics.AuthUsed != 0 || payload.Metrics.OGProxyServed != 0 {
		t.Fatalf("legacy dependency counters unexpectedly non-zero: %+v", payload.Metrics)
	}
}

func TestHealthHandlerDoesNotExposeExperimentMetricsInLegacyMode(t *testing.T) {
	old := observability.Default
	observability.Configure(observability.Config{})
	t.Cleanup(func() { observability.Default = old })

	t.Setenv("INSTAFIX_EXPERIMENT_MODE", "")
	t.Setenv("INSTAFIX_EXPERIMENT_LABEL", "")
	rec := httptest.NewRecorder()
	healthHandler(rec, httptest.NewRequest(http.MethodGet, "https://instagram7.test/healthz", nil))

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode health JSON: %v", err)
	}
	if _, ok := payload["metrics"]; ok {
		t.Fatal("legacy health response must not expose experiment metrics")
	}
}
