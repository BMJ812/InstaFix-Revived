package handlers

import "log/slog"

// RefreshVideoFromPublicGraphQLPreserveMetadata forces a cookie-free public
// video refresh while retaining metadata that the public GraphQL response may
// omit. RefreshVideoFromPublicGraphQL saves its raw result first; this wrapper
// immediately replaces that cache entry with the merged result when possible so
// subsequent stateless requests do not lose a caption/username solely because a
// signed CDN URL needed renewal. A cache write failure must never make a fresh
// direct CDN URL unusable.
func RefreshVideoFromPublicGraphQLPreserveMetadata(postID string, previous *InstaData) (*InstaData, error) {
	refreshed, err := RefreshVideoFromPublicGraphQL(postID)
	if err != nil {
		return nil, err
	}
	mergeAvailableMetadata(refreshed, previous)
	mergeMediaPresentation(refreshed, previous)
	if err := saveDataToCache(refreshed); err != nil {
		slog.Debug("Failed to cache metadata-preserving stateless video refresh", "postID", postID, "err", err)
	}
	return refreshed, nil
}
