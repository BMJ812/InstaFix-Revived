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
	want := "Views 1,234 | Likes 0\n\ncaption"
	if got != want {
		t.Fatalf("embedDescription() = %q, want %q", got, want)
	}
}
