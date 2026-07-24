package views

import (
	"bytes"
	"strings"
	"testing"

	"instafix/views/model"
)

func TestEmbedVideoUsesOGVideoWithoutTwitterPlayerCard(t *testing.T) {
	var buf bytes.Buffer
	Embed(&model.ViewsData{
		Card:        "summary_large_image",
		Title:       "@user",
		ImageURL:    "https://example.com/offload/CODE/1?thumbnail=1",
		VideoURL:    "https://example.com/offload/CODE/1",
		URL:         "https://instagram.com/reel/CODE/",
		Description: "caption",
		OGType:      "article",
		Width:       720,
		Height:      1280,
	}, &buf)

	html := buf.String()
	for _, want := range []string{
		`<meta name="twitter:card" content="summary_large_image"/>`,
		`<meta property="og:type" content="article"/>`,
		`<meta property="og:video" content="https://example.com/offload/CODE/1"/>`,
		`<meta property="og:video:secure_url" content="https://example.com/offload/CODE/1"/>`,
		`<meta property="og:video:type" content="video/mp4"/>`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("embed HTML missing %s in:\n%s", want, html)
		}
	}
	for _, unwanted := range []string{
		`name="twitter:player"`,
		`name="twitter:player:stream"`,
		`name="twitter:player:width"`,
		`name="twitter:player:height"`,
	} {
		if strings.Contains(html, unwanted) {
			t.Fatalf("video embed must not contain %s:\n%s", unwanted, html)
		}
	}
}
