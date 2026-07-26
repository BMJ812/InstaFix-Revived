package handlers

import (
	scraper "instafix/handlers/scraper"
	"testing"
)

func TestEmbedDescriptionIncludesOnlyAvailableStats(t *testing.T) {
	item := &scraper.InstaData{
		Caption:         "caption",
		ViewCount:       1234,
		LikeCount:       0,
		HasViewCount:    true,
		HasLikeCount:    true,
		HasCommentCount: false,
	}
	got := embedDescription(item)
	want := "▶️ 1,234  ❤️ 0\n\ncaption"
	if got != want {
		t.Fatalf("embedDescription() = %q, want %q", got, want)
	}
}

func TestAuthFallbackOnlyReplacesUsableReelWhenItAddsVideo(t *testing.T) {
	public := &scraper.InstaData{
		Username:     "public",
		Medias:       []scraper.Media{{TypeName: "GraphImage"}, {TypeName: "GraphImage"}},
		LikeCount:    93,
		HasLikeCount: true,
	}
	authImage := &scraper.InstaData{
		Username: "auth",
		Medias:   []scraper.Media{{TypeName: "GraphImage"}},
	}
	authVideo := &scraper.InstaData{
		Username: "auth",
		Medias:   []scraper.Media{{TypeName: "GraphVideo"}},
	}

	if shouldUseAuthFallbackItem(public, authImage, true) {
		t.Fatal("image-only auth result replaced richer public Reel metadata")
	}
	if !shouldUseAuthFallbackItem(public, authVideo, true) {
		t.Fatal("auth video did not replace public image fallback")
	}
	if !shouldUseAuthFallbackItem(nil, authImage, true) {
		t.Fatal("auth image should fill an otherwise missing preview")
	}
}

func TestPreviewVideoRouteVersionsConfiguredCDNRedirect(t *testing.T) {
	oldEnabled := PreviewVideoCDNRedirectEnabled
	oldAgents := PreviewVideoCDNRedirectUserAgents
	ConfigurePreviewVideoCDNRedirect(true, "telegrambot")
	t.Cleanup(func() {
		PreviewVideoCDNRedirectEnabled = oldEnabled
		PreviewVideoCDNRedirectUserAgents = oldAgents
	})

	got := previewVideoRoute("https://fix.example", "DbLarge1", 1, "TelegramBot (like TwitterBot)")
	want := "https://fix.example/offload/DbLarge1/1?delivery=cdn-redirect-v1"
	if got != want {
		t.Fatalf("redirect route = %q, want %q", got, want)
	}
	if got := previewVideoRoute("https://fix.example", "DbLarge1", 1, "Discordbot/2.0"); got != "https://fix.example/offload/DbLarge1/1" {
		t.Fatalf("non-configured route = %q", got)
	}
}
