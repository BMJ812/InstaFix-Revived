package handlers

import (
	"errors"
	"log/slog"
)

// RefreshDataNoAuthPreserveMetadata bypasses cached/stale data and performs a
// fresh anonymous scrape. It is used by the stateless renderer when a signed
// CDN object is expired or entering its safety window. No auth-helper, cookies,
// or remote scraper are involved.
func RefreshDataNoAuthPreserveMetadata(postID string, previous *InstaData) (*InstaData, error) {
	if !validPostID(postID) {
		return nil, errors.New("postID is not a valid Instagram post ID")
	}

	item := &InstaData{PostID: postID}
	if err := item.ScrapeDataNoAuth(); err != nil {
		return nil, err
	}
	if len(item.Medias) == 0 {
		return nil, ErrNotFound
	}
	if err := normalizeMediaURLs(item); err != nil {
		return nil, err
	}

	mergeAvailableMetadata(item, previous)
	mergeMediaPresentation(item, previous)
	if err := saveDataToCache(item); err != nil {
		slog.Debug("Failed to cache metadata-preserving stateless public refresh", "postID", postID, "err", err)
	}
	return item, nil
}
