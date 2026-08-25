package handlers

import (
	scraper "instafix/handlers/scraper"
	"testing"
	"time"
)

func TestStatelessGridImageURLsRejectsExpiredAndMalformedSignedMedia(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	item := &scraper.InstaData{Medias: []scraper.Media{
		{
			TypeName: "GraphImage",
			URL:      "https://scontent.cdninstagram.com/fresh.jpg?oe=" + upperHex(now.Add(time.Hour).Unix()),
		},
		{
			TypeName: "GraphImage",
			URL:      "https://scontent.cdninstagram.com/expired.jpg?oe=" + upperHex(now.Add(-time.Minute).Unix()),
		},
		{
			TypeName: "GraphImage",
			URL:      "https://scontent.cdninstagram.com/malformed.jpg?oe=not-hex",
		},
		{
			TypeName: "GraphVideo",
			URL:      "https://scontent.cdninstagram.com/video.mp4?oe=" + upperHex(now.Add(time.Hour).Unix()),
		},
		{
			TypeName: "GraphImage",
			URL:      "https://scontent.cdninstagram.com/unsigned.jpg",
		},
	}}

	got := statelessGridImageURLs(item, now)
	if len(got) != 2 {
		t.Fatalf("statelessGridImageURLs() returned %d URLs, want 2: %v", len(got), got)
	}
	if got[0] != item.Medias[0].URL || got[1] != item.Medias[4].URL {
		t.Fatalf("unexpected grid URLs: %v", got)
	}
}

func TestStatelessGridImageURLsRejectsNonInstagramHosts(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	item := &scraper.InstaData{Medias: []scraper.Media{
		{TypeName: "GraphImage", URL: "https://example.com/image.jpg"},
		{TypeName: "GraphImage", URL: "http://scontent.cdninstagram.com/image.jpg"},
	}}
	if got := statelessGridImageURLs(item, now); len(got) != 0 {
		t.Fatalf("unexpected non-CDN grid URLs: %v", got)
	}
}
