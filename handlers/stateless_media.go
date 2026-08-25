package handlers

import (
	"errors"
	"fmt"
	scraper "instafix/handlers/scraper"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var statelessMinPlayableURLLife = 2 * time.Minute

// selectStatelessMedia renews a direct media URL when it is invalid or entering
// the same safety window used by edge caching. Video first gets the dedicated
// public GraphQL refresh; all media types can then fall back to a fresh anonymous
// scrape. If refresh fails we may still use the old URL when it has a small but
// safe amount of life remaining. Expired/nearly-expired media is never advertised.
func selectStatelessMedia(postID string, selected int, item *scraper.InstaData, now time.Time) (*scraper.InstaData, scraper.Media, error) {
	if item == nil || selected < 0 || selected >= len(item.Medias) {
		return item, scraper.Media{}, errors.New("media index out of range")
	}
	media := item.Medias[selected]

	remaining, signed := statelessSignedURLRemaining(media.URL, now)
	needsRefresh := !isInstagramCDNURL(media.URL) || (signed && remaining <= statelessSignedURLMargin)
	if !needsRefresh && statelessMediaURLPlayable(media.URL, now) {
		return item, media, nil
	}

	var refreshErr error
	if media.IsVideo() {
		refreshed, err := scraper.RefreshVideoFromPublicGraphQLPreserveMetadata(postID, item)
		refreshErr = err
		if err == nil {
			if candidate, ok := statelessCandidateMedia(refreshed, selected, true, now); ok {
				return refreshed, candidate, nil
			}
			refreshErr = errors.New("public GraphQL refresh did not return a playable video URL")
		}
	}

	refreshed, genericErr := scraper.RefreshDataNoAuthPreserveMetadata(postID, item)
	if genericErr == nil {
		if candidate, ok := statelessCandidateMedia(refreshed, selected, media.IsVideo(), now); ok {
			return refreshed, candidate, nil
		}
		genericErr = errors.New("anonymous refresh did not return a playable matching media URL")
	}
	if refreshErr == nil {
		refreshErr = genericErr
	} else if genericErr != nil {
		refreshErr = fmt.Errorf("public GraphQL refresh: %v; anonymous refresh: %w", refreshErr, genericErr)
	}

	if statelessMediaURLPlayable(media.URL, now) {
		// Refresh inside the larger edge-cache safety margin is proactive. A URL
		// that still has the smaller playable safety margin may be served once;
		// the HTML will be no-store at the edge.
		return item, media, nil
	}
	if refreshErr == nil {
		refreshErr = errors.New("direct media URL is expired or too close to expiry")
	}
	return item, media, fmt.Errorf("stateless direct media refresh failed: %w", refreshErr)
}

func statelessCandidateMedia(item *scraper.InstaData, selected int, wantVideo bool, now time.Time) (scraper.Media, bool) {
	if item == nil || selected < 0 || selected >= len(item.Medias) {
		return scraper.Media{}, false
	}
	candidate := item.Medias[selected]
	if candidate.IsVideo() != wantVideo || !statelessMediaURLPlayable(candidate.URL, now) {
		return scraper.Media{}, false
	}
	return candidate, true
}

func statelessMediaURLPlayable(raw string, now time.Time) bool {
	if !isInstagramCDNURL(raw) {
		return false
	}
	remaining, signed := statelessSignedURLRemaining(raw, now)
	return !signed || remaining > statelessMinPlayableURLLife
}

func statelessSignedURLRemaining(raw string, now time.Time) (time.Duration, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return 0, false
	}
	oe := strings.TrimSpace(u.Query().Get("oe"))
	if oe == "" {
		return 0, false
	}
	seconds, err := strconv.ParseInt(oe, 16, 64)
	if err != nil || seconds <= 0 {
		// The URL claims to be signed but has an unusable expiry. Treat it as
		// already expired so it can never bypass refresh as an "unsigned" URL.
		return -time.Second, true
	}
	return time.Unix(seconds, 0).Sub(now), true
}
