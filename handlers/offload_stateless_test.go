package handlers

import (
	"errors"
	"fmt"
	scraper "instafix/handlers/scraper"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOffloadStatelessRedirectsWithoutRelayingVideoBytes(t *testing.T) {
	oldQuiet := offloadGetDataPreferVideoQuiet
	oldRefresh := offloadRefreshDataPreferVideo
	oldAnonymousRefresh := offloadRefreshDataNoAuthPreserveMetadata
	defer func() {
		offloadGetDataPreferVideoQuiet = oldQuiet
		offloadRefreshDataPreferVideo = oldRefresh
		offloadRefreshDataNoAuthPreserveMetadata = oldAnonymousRefresh
	}()

	videoURL := "https://scontent.cdninstagram.com/video.mp4?oe=FFFFFFFF"
	offloadGetDataPreferVideoQuiet = func(string) (*scraper.InstaData, error) {
		return &scraper.InstaData{Medias: []scraper.Media{{TypeName: "GraphVideo", URL: videoURL}}}, nil
	}
	offloadRefreshDataPreferVideo = func(string) (*scraper.InstaData, error) {
		t.Fatal("fresh CDN URL should not require refresh")
		return nil, nil
	}
	offloadRefreshDataNoAuthPreserveMetadata = func(string, *scraper.InstaData) (*scraper.InstaData, error) {
		t.Fatal("fresh CDN URL should not require anonymous refresh")
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

func TestOffloadStatelessRefreshesMissingImageCarouselIndex(t *testing.T) {
	oldQuiet := offloadGetDataPreferVideoQuiet
	oldRefresh := offloadRefreshDataPreferVideo
	oldAnonymousRefresh := offloadRefreshDataNoAuthPreserveMetadata
	defer func() {
		offloadGetDataPreferVideoQuiet = oldQuiet
		offloadRefreshDataPreferVideo = oldRefresh
		offloadRefreshDataNoAuthPreserveMetadata = oldAnonymousRefresh
	}()

	cached := &scraper.InstaData{Medias: imageCarousel(6)}
	fresh := &scraper.InstaData{Medias: imageCarousel(13)}
	offloadGetDataPreferVideoQuiet = func(string) (*scraper.InstaData, error) {
		return cached, nil
	}
	anonymousRefreshCalls := 0
	offloadRefreshDataNoAuthPreserveMetadata = func(postID string, previous *scraper.InstaData) (*scraper.InstaData, error) {
		anonymousRefreshCalls++
		if postID != "DcbpfNACkUK" {
			t.Fatalf("postID = %q", postID)
		}
		if previous != cached {
			t.Fatal("anonymous refresh did not receive cached metadata")
		}
		return fresh, nil
	}
	offloadRefreshDataPreferVideo = func(string) (*scraper.InstaData, error) {
		t.Fatal("image-only carousel must not require video refresh")
		return nil, nil
	}

	req := offloadRequest(t, http.MethodHead, "https://stateless.instagram7.test/offload/DcbpfNACkUK/7", "DcbpfNACkUK", "7")
	rec := httptest.NewRecorder()
	OffloadStateless(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	want := fresh.Medias[6].URL
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
	if anonymousRefreshCalls != 1 {
		t.Fatalf("anonymous refresh calls = %d, want 1", anonymousRefreshCalls)
	}
}

func TestOffloadStatelessReturnsNotFoundAfterCompleteCarouselRefresh(t *testing.T) {
	oldQuiet := offloadGetDataPreferVideoQuiet
	oldRefresh := offloadRefreshDataPreferVideo
	oldAnonymousRefresh := offloadRefreshDataNoAuthPreserveMetadata
	defer func() {
		offloadGetDataPreferVideoQuiet = oldQuiet
		offloadRefreshDataPreferVideo = oldRefresh
		offloadRefreshDataNoAuthPreserveMetadata = oldAnonymousRefresh
	}()

	cached := &scraper.InstaData{Medias: imageCarousel(6)}
	fresh := &scraper.InstaData{Medias: imageCarousel(13)}
	offloadGetDataPreferVideoQuiet = func(string) (*scraper.InstaData, error) {
		return cached, nil
	}
	offloadRefreshDataNoAuthPreserveMetadata = func(string, *scraper.InstaData) (*scraper.InstaData, error) {
		return fresh, nil
	}
	offloadRefreshDataPreferVideo = func(string) (*scraper.InstaData, error) {
		t.Fatal("out-of-range image carousel must not require video refresh")
		return nil, nil
	}

	req := offloadRequest(t, http.MethodHead, "https://stateless.instagram7.test/offload/DcbpfNACkUK/14", "DcbpfNACkUK", "14")
	rec := httptest.NewRecorder()
	OffloadStateless(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestOffloadStatelessRetainsVideoSpecificFallback(t *testing.T) {
	oldQuiet := offloadGetDataPreferVideoQuiet
	oldRefresh := offloadRefreshDataPreferVideo
	oldAnonymousRefresh := offloadRefreshDataNoAuthPreserveMetadata
	defer func() {
		offloadGetDataPreferVideoQuiet = oldQuiet
		offloadRefreshDataPreferVideo = oldRefresh
		offloadRefreshDataNoAuthPreserveMetadata = oldAnonymousRefresh
	}()

	cached := &scraper.InstaData{Medias: []scraper.Media{{TypeName: "GraphVideo", URL: "https://example.com/stale.mp4"}}}
	videoURL := "https://scontent.cdninstagram.com/refreshed.mp4?oe=FFFFFFFF"
	offloadGetDataPreferVideoQuiet = func(string) (*scraper.InstaData, error) {
		return cached, nil
	}
	offloadRefreshDataNoAuthPreserveMetadata = func(string, *scraper.InstaData) (*scraper.InstaData, error) {
		return nil, errors.New("anonymous refresh unavailable")
	}
	offloadRefreshDataPreferVideo = func(string) (*scraper.InstaData, error) {
		return &scraper.InstaData{Medias: []scraper.Media{{TypeName: "GraphVideo", URL: videoURL}}}, nil
	}

	req := offloadRequest(t, http.MethodHead, "https://stateless.instagram7.test/offload/VideoRefresh01/1", "VideoRefresh01", "1")
	rec := httptest.NewRecorder()
	OffloadStateless(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	if got := rec.Header().Get("Location"); got != videoURL {
		t.Fatalf("Location = %q, want %q", got, videoURL)
	}
}

func imageCarousel(count int) []scraper.Media {
	medias := make([]scraper.Media, count)
	for i := range medias {
		medias[i] = scraper.Media{
			TypeName: "GraphImage",
			URL:      fmt.Sprintf("https://scontent.cdninstagram.com/carousel-%02d.jpg?oe=FFFFFFFF", i+1),
		}
	}
	return medias
}
