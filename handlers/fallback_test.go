package handlers

import (
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFallbackPreviewDimensions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		url    string
		width  int
		height int
	}{
		{name: "generic", url: "https://instagram7.test/fallback/Code.png", width: 1200, height: 630},
		{name: "reel", url: "https://instagram7.test/fallback/Code.png?kind=reel", width: 720, height: 1280},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			FallbackPreview(recorder, httptest.NewRequest(http.MethodGet, tc.url, nil))
			config, err := png.DecodeConfig(recorder.Body)
			if err != nil {
				t.Fatalf("decode fallback PNG: %v", err)
			}
			if config.Width != tc.width || config.Height != tc.height {
				t.Fatalf("fallback dimensions = %dx%d, want %dx%d", config.Width, config.Height, tc.width, tc.height)
			}
		})
	}
}
