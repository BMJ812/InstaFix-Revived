package handlers

import (
	scraper "instafix/handlers/scraper"
	"testing"
	"time"
)

func TestIsInstagramCDNURL(t *testing.T) {
	for _, raw := range []string{
		"https://scontent.cdninstagram.com/v/t50.2886-16/example.mp4",
		"https://instagram.fvil1-1.fna.fbcdn.net/v/example.jpg",
	} {
		if !isInstagramCDNURL(raw) {
			t.Fatalf("expected Instagram CDN URL to be accepted: %s", raw)
		}
	}
	for _, raw := range []string{
		"http://scontent.cdninstagram.com/example.mp4",
		"https://example.com/video.mp4",
		"https://www.instagram7.com/offload/ABCDEF/1.mp4",
		"https://www.instagram7.com/videos/ABCDEF/1",
		"javascript:alert(1)",
	} {
		if isInstagramCDNURL(raw) {
			t.Fatalf("expected URL to be rejected: %s", raw)
		}
	}
}

func TestStatelessMediaURLPlayableChecksSignedExpiry(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	fresh := "https://scontent.cdninstagram.com/video.mp4?oe=" + upperHex(now.Add(15*time.Minute).Unix())
	nearExpiry := "https://scontent.cdninstagram.com/video.mp4?oe=" + upperHex(now.Add(time.Minute).Unix())
	expired := "https://scontent.cdninstagram.com/video.mp4?oe=" + upperHex(now.Add(-time.Minute).Unix())
	malformed := "https://scontent.cdninstagram.com/video.mp4?oe=not-hex"

	if !statelessMediaURLPlayable(fresh, now) {
		t.Fatal("fresh signed CDN URL should be playable")
	}
	for _, raw := range []string{nearExpiry, expired, malformed} {
		if statelessMediaURLPlayable(raw, now) {
			t.Fatalf("URL should not be advertised as directly playable: %s", raw)
		}
	}
}

func TestStatelessEdgeTTLUsesSignedExpiryMargin(t *testing.T) {
	oldTTL, oldMargin := statelessNormalEdgeTTL, statelessSignedURLMargin
	defer func() {
		statelessNormalEdgeTTL = oldTTL
		statelessSignedURLMargin = oldMargin
	}()
	statelessNormalEdgeTTL = 30 * time.Minute
	statelessSignedURLMargin = 10 * time.Minute

	now := time.Unix(1_800_000_000, 0)
	expires := now.Add(20 * time.Minute)
	item := &scraper.InstaData{Medias: []scraper.Media{{
		TypeName: "GraphVideo",
		URL:      "https://scontent.cdninstagram.com/example.mp4?oe=" + upperHex(expires.Unix()),
	}}}

	got := statelessEdgeTTL(item, now)
	if want := 10 * time.Minute; got != want {
		t.Fatalf("statelessEdgeTTL() = %s, want %s", got, want)
	}
}

func TestStatelessEdgeTTLDisablesCacheNearExpiry(t *testing.T) {
	oldTTL, oldMargin := statelessNormalEdgeTTL, statelessSignedURLMargin
	defer func() {
		statelessNormalEdgeTTL = oldTTL
		statelessSignedURLMargin = oldMargin
	}()
	statelessNormalEdgeTTL = 5 * time.Minute
	statelessSignedURLMargin = 30 * time.Minute

	now := time.Unix(1_800_000_000, 0)
	expires := now.Add(20 * time.Minute)
	item := &scraper.InstaData{Medias: []scraper.Media{{
		TypeName: "GraphVideo",
		URL:      "https://scontent.cdninstagram.com/example.mp4?oe=" + upperHex(expires.Unix()),
	}}}
	if got := statelessEdgeTTL(item, now); got != 0 {
		t.Fatalf("statelessEdgeTTL() = %s, want 0", got)
	}
}

func upperHex(v int64) string {
	const digits = "0123456789ABCDEF"
	if v == 0 {
		return "0"
	}
	buf := make([]byte, 0, 16)
	for v > 0 {
		buf = append(buf, digits[v&15])
		v >>= 4
	}
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf)
}
