package handlers

import (
	"errors"
	scraper "instafix/handlers/scraper"
	"strings"
	"testing"
)

func TestFallbackPreviewCopy(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantTitle  string
		wantInBody string
	}{
		{
			name:       "21 plus restriction",
			err:        errors.Join(scraper.ErrRestricted, errors.New("People under 21 can't see this content (MIN_AGE_ACCOUNT)")),
			wantTitle:  "Instagram post is age-restricted",
			wantInBody: "21 or older",
		},
		{
			name:       "region restriction",
			err:        errors.Join(scraper.ErrRestricted, errors.New("geoblock_required")),
			wantTitle:  "Instagram post is region-restricted",
			wantInBody: "region",
		},
		{
			name:       "not found",
			err:        scraper.ErrNotFound,
			wantTitle:  "Instagram post not available",
			wantInBody: "deleted",
		},
		{
			name:       "rate limited",
			err:        errors.New("Instagram HTTP 429 too many requests"),
			wantTitle:  "Instagram is temporarily limiting requests",
			wantInBody: "rate-limited",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			title, body := fallbackPreviewCopy(tc.err)
			if title != tc.wantTitle {
				t.Fatalf("title = %q, want %q", title, tc.wantTitle)
			}
			if !strings.Contains(body, tc.wantInBody) {
				t.Fatalf("body = %q, want substring %q", body, tc.wantInBody)
			}
		})
	}
}
