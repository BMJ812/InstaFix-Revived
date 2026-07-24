package views

import (
	"bytes"
	"strings"
	"testing"

	"instafix/views/model"
)

func TestEmbedVideoUsesSeparatePlayerAndDirectStreamURLs(t *testing.T) {
	var buf bytes.Buffer
	Embed(&model.ViewsData{
		Card:        "player",
		Title:       "@user",
		ImageURL:    "https://example.com/offload/CODE/1?thumbnail=1",
		VideoURL:    "https://example.com/offload/CODE/1",
		PlayerURL:   "https://example.com/player/CODE/1",
		URL:         "https://instagram.com/reel/CODE/",
		Description: "caption",
		OGType:      "video.other",
		Width:       720,
		Height:      1280,
	}, &buf)

	html := buf.String()
	for _, want := range []string{
		`<meta name="twitter:card" content="player"/>`,
		`<meta name="twitter:player" content="https://example.com/player/CODE/1"/>`,
		`<meta name="twitter:player:stream" content="https://example.com/offload/CODE/1"/>`,
		`<meta property="og:video" content="https://example.com/offload/CODE/1"/>`,
		`<meta property="og:video:secure_url" content="https://example.com/offload/CODE/1"/>`,
		`<meta property="og:video:type" content="video/mp4"/>`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("embed HTML missing %s in:\n%s", want, html)
		}
	}
	if strings.Contains(html, `name="twitter:player" content="https://example.com/offload/`) {
		t.Fatalf("twitter:player must point to HTML, not MP4:\n%s", html)
	}
}
