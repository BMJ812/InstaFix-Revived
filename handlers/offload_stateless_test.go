package handlers

import (
	scraper "instafix/handlers/scraper"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOffloadStatelessRedirectsWithoutRelayingVideoBytes(t *testing.T) {
	oldQuiet := offloadGetDataPreferVideoQuiet
	oldRefresh := offloadRefreshDataPreferVideo
	defer func() {
		offloadGetDataPreferVideoQuiet = oldQuiet
		offloadRefreshDataPreferVideo = oldRefresh
	}()

	videoURL := "https://scontent.cdninstagram.com/video.mp4?oe=FFFFFFFF"
	offloadGetDataPreferVideoQuiet = func(string) (*scraper.InstaData, error) {
		return &scraper.InstaData{Medias: []scraper.Media{{TypeName: "GraphVideo", URL: videoURL}}}, nil
	}
	offloadRefreshDataPreferVideo = func(string) (*scraper.InstaData, error) {
		t.Fatal("fresh CDN URL should not require refresh")
		return nil, nil
	}

	req := offloadRequest(t, http.MethodGet, "https://stateless.instagram7.test/offload/DbRedirect01/1", "DbRedirect01", "1")
	rec := httptest.NewRecorder()
	OffloadStateless(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	if got := rec.Header().Get("Location"); got != videoURL {
		t.Fatalf("Location = %q, want %q", got, videoURL)
	}
	if got := rec.Header().Get("X-InstaFix-Video-Delivery"); got != "stateless-cdn-redirect" {
		t.Fatalf("delivery = %q", got)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("redirect unexpectedly relayed %d body bytes", rec.Body.Len())
	}
}
