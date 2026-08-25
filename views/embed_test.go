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
		Card:          "summary_large_image",
		Title:         "@user",
		ImageURL:      "https://instagram7.com/offload/CODE/1?thumbnail=1",
		VideoURL:      "https://instagram7.com/offload/CODE/1",
		URL:           "https://instagram.com/reel/CODE/",
		Description:   "caption",
		OGType:        "article",
		FaviconURL:    "https://instagram7.com/favicon.svg",
		AppleIconURL:  "https://instagram7.com/favicon.svg",
		ArticleAuthor: "https://www.instagram.com/user/",
		Width:         720,
		Height:        1280,
	}, &buf)

	html := buf.String()
	for _, want := range []string{
		`<meta name="twitter:card" content="summary_large_image"/>`,
		`<meta property="og:type" content="article"/>`,
		`<meta property="og:video" content="https://instagram7.com/offload/CODE/1"/>`,
		`<meta property="og:video:secure_url" content="https://instagram7.com/offload/CODE/1"/>`,
		`<meta property="og:video:type" content="video/mp4"/>`,
		`<link href="https://instagram7.com/favicon.svg" rel="icon" sizes="any" type="image/svg+xml"/>`,
		`<link rel="apple-touch-icon" href="https://instagram7.com/favicon.svg"/>`,
		`<meta property="article:author" content="https://www.instagram.com/user/"/>`,
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

func TestEmbedVideoOmitsUnknownDimensions(t *testing.T) {
	var buf bytes.Buffer
	Embed(&model.ViewsData{
		Card:       "summary_large_image",
		Title:      "Instagram Reel",
		VideoURL:   "https://d.oginstagram.example/p/CODE/",
		URL:        "https://instagram.com/reel/CODE/",
		OGType:     "article",
		NoRedirect: true,
	}, &buf)

	html := buf.String()
	if !strings.Contains(html, `property="og:video:type" content="video/mp4"`) {
		t.Fatalf("video type missing from unknown-dimension embed:\n%s", html)
	}
	for _, unwanted := range []string{`property="og:video:width"`, `property="og:video:height"`} {
		if strings.Contains(html, unwanted) {
			t.Fatalf("unknown-dimension embed must not contain %s:\n%s", unwanted, html)
		}
	}
}

func TestEmbedTelegramPlayerIncludesHTMLPlayerAndMP4Stream(t *testing.T) {
	var buf bytes.Buffer
	Embed(&model.ViewsData{
		Card:        "player",
		Title:       "@user",
		ImageURL:    "https://instagram7.com/offload/CODE/1?thumbnail=1",
		VideoURL:    "https://instagram7.com/offload/CODE/1.mp4?delivery=test",
		PlayerURL:   "https://instagram7.com/player/CODE/1",
		URL:         "https://instagram.com/reel/CODE/",
		Description: "caption",
		OGType:      "video.other",
		Width:       608,
		Height:      1080,
	}, &buf)

	html := buf.String()
	for _, want := range []string{
		`<meta property="og:type" content="video.other"/>`,
		`<meta name="twitter:card" content="player"/>`,
		`<meta name="twitter:player" content="https://instagram7.com/player/CODE/1"/>`,
		`<meta name="twitter:player:width" content="608"/>`,
		`<meta name="twitter:player:height" content="1080"/>`,
		`<meta name="twitter:player:stream" content="https://instagram7.com/offload/CODE/1.mp4?delivery=test"/>`,
		`<meta name="twitter:player:stream:content_type" content="video/mp4"/>`,
		`<meta property="og:video" content="https://instagram7.com/offload/CODE/1.mp4?delivery=test"/>`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("Telegram player HTML missing %s in:\n%s", want, html)
		}
	}
}

func TestEmbedCanSuppressMetaRefresh(t *testing.T) {
	var buf bytes.Buffer
	Embed(&model.ViewsData{
		Card:       "summary_large_image",
		Title:      "@user",
		ImageURL:   "https://instagram7.com/offload/CODE/1?thumbnail=1",
		VideoURL:   "https://instagram7.com/offload/CODE/1",
		URL:        "https://instagram.com/reel/CODE/",
		OGType:     "article",
		NoRedirect: true,
		Width:      720,
		Height:     1280,
	}, &buf)

	html := buf.String()
	if strings.Contains(html, `http-equiv="refresh"`) {
		t.Fatalf("embed HTML should not include meta refresh:\n%s", html)
	}
	if !strings.HasSuffix(html, `</head><body></body></html>`) {
		t.Fatalf("embed HTML should close with an empty body:\n%s", html)
	}
}
