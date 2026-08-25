package handlers

import (
	"errors"
	scraper "instafix/handlers/scraper"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// OffloadStateless keeps preview metadata on a stable same-origin URL. Normal
// media requests stay redirect-only; the compact=av1/av2 Telegram fallback is the
// one deliberate exception and serves a small remuxed MP4 with audio.
func OffloadStateless(w http.ResponseWriter, r *http.Request) {
	postID := chi.URLParam(r, "postID")
	mediaNum, err := strconv.Atoi(strings.TrimSuffix(chi.URLParam(r, "mediaNum"), ".mp4"))
	if err != nil || mediaNum < 1 {
		http.Error(w, "invalid media number", http.StatusBadRequest)
		return
	}

	thumbnail := r.URL.Query().Has("thumbnail")
	if compact := r.URL.Query().Get("compact"); !thumbnail && mediaNum == 1 && (compact == "av1" || compact == "av2" || compact == "av3" || compact == "av4") {
		ServeCompactAV(w, r, postID)
		return
	}
	item, fetchErr := offloadGetDataPreferVideoQuiet(postID)
	target, media, ok := statelessOffloadTarget(item, mediaNum, thumbnail, time.Now())
	if !ok {
		refreshed, refreshErr := offloadRefreshDataPreferVideo(postID)
		if refreshErr != nil {
			if fetchErr != nil {
				refreshErr = errors.Join(fetchErr, refreshErr)
			}
			slog.Warn("stateless offload refresh failed", "postID", postID, "media_index", mediaNum, "err", refreshErr)
			http.Error(w, "media temporarily unavailable", http.StatusBadGateway)
			return
		}
		target, media, ok = statelessOffloadTarget(refreshed, mediaNum, thumbnail, time.Now())
	}
	if !ok {
		http.Error(w, "media URL unavailable", http.StatusNotFound)
		return
	}

	// Diagnostic/compatibility path: the normal stateless offload remains a
	// cheap 302 to Instagram CDN. Only an explicitly advertised .mp4 URL fetched
	// by Telegram uses the existing minimal stream, returning same-origin 200
	// video/mp4 with Range/upstream transport headers stripped. Current default
	// embeds never advertise .mp4, so compact=av4 and normal direct delivery are
	// unaffected.
	if media.IsVideo() && !thumbnail && isTelegramBot(r.UserAgent()) && strings.HasSuffix(strings.ToLower(r.URL.Path), ".mp4") {
		streamOffloadVideo(w, r, postID, mediaNum, target)
		return
	}

	w.Header().Set("Cache-Control", "public, max-age=60")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Location", target)
	w.Header().Set("X-Instagram7-Experiment", "stateless-azure")
	if media.IsVideo() && !thumbnail {
		w.Header().Set("X-InstaFix-Video-Delivery", "stateless-cdn-redirect")
	}
	w.WriteHeader(http.StatusFound)
	slog.Info("stateless offload redirected to CDN",
		"postID", postID,
		"media_index", mediaNum,
		"thumbnail", thumbnail,
		"video", media.IsVideo(),
		"status", http.StatusFound,
	)
}

func statelessOffloadTarget(item *scraper.InstaData, mediaNum int, thumbnail bool, now time.Time) (string, scraper.Media, bool) {
	if item == nil || mediaNum < 1 || mediaNum > len(item.Medias) {
		return "", scraper.Media{}, false
	}
	media := item.Medias[mediaNum-1]
	target := media.URL
	if thumbnail && media.ThumbnailURL != "" {
		target = media.ThumbnailURL
	}
	if !statelessMediaURLPlayable(target, now) {
		return "", media, false
	}
	return target, media, true
}
