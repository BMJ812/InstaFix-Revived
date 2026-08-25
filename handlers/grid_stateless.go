package handlers

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	scraper "instafix/handlers/scraper"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
)

// GridStateless generates carousel preview JPEGs entirely in memory. The
// response is intended to be cached by Cloudflare; no file under static/ is
// created and a container restart requires no grid-cache restoration.
func GridStateless(w http.ResponseWriter, r *http.Request) {
	postID := chi.URLParam(r, "postID")
	item, err := scraper.GetDataQuiet(postID)
	if err != nil || item == nil {
		if err == nil {
			err = scraper.ErrNotFound
		}
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	mediaURLs := statelessGridImageURLs(item, time.Now())
	// A stale signed image URL can survive the short compatibility cache. Refresh
	// the carousel anonymously before deciding that the grid is unavailable.
	if len(mediaURLs) < 2 && len(item.Medias) > 1 {
		if refreshed, refreshErr := scraper.RefreshDataNoAuthPreserveMetadata(postID, item); refreshErr == nil && refreshed != nil {
			item = refreshed
			mediaURLs = statelessGridImageURLs(item, time.Now())
		} else if refreshErr != nil {
			slog.Debug("stateless grid anonymous refresh failed", "postID", postID, "err", refreshErr)
		}
	}
	if len(mediaURLs) == 0 {
		http.Error(w, "no fresh images for grid", http.StatusNotFound)
		return
	}
	if len(mediaURLs) == 1 {
		http.Redirect(w, r, mediaURLs[0], http.StatusFound)
		return
	}
	if len(mediaURLs) > maxGridImages {
		slog.Warn("stateless grid generation skipped: too many images", "postID", postID, "count", len(mediaURLs), "max", maxGridImages)
		http.Redirect(w, r, mediaURLs[0], http.StatusFound)
		return
	}

	result, err, _ := sflightGrid.Do("stateless:"+postID, func() (interface{}, error) {
		client := http.Client{Transport: transport, Timeout: timeout}
		images := make([]image.Image, 0, len(mediaURLs))
		var totalPixels int64
		for _, mediaURL := range mediaURLs {
			img, pixels, err := decodeGridImage(&client, postID, mediaURL)
			if err != nil {
				return nil, err
			}
			if totalPixels+pixels > maxGridTotalPixels {
				return nil, fmt.Errorf("grid total image dimensions too large: %d pixels", totalPixels+pixels)
			}
			totalPixels += pixels
			images = append(images, img)
		}

		grid, err := GenerateGrid(images)
		if err != nil {
			return nil, err
		}
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, grid, &jpeg.Options{Quality: 80}); err != nil {
			return nil, err
		}
		return append([]byte(nil), buf.Bytes()...), nil
	})
	if err != nil {
		slog.Warn("stateless grid generation failed; redirecting to first image", "postID", postID, "err", err)
		http.Redirect(w, r, mediaURLs[0], http.StatusFound)
		return
	}

	jpegBytes, ok := result.([]byte)
	if !ok || len(jpegBytes) == 0 {
		http.Error(w, "empty grid image", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("CDN-Cache-Control", "public, max-age=86400")
	w.Header().Set("Cloudflare-CDN-Cache-Control", "public, max-age=86400")
	w.Header().Set("Content-Length", strconv.Itoa(len(jpegBytes)))
	w.Header().Set("X-Instagram7-Experiment", "stateless-azure")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(jpegBytes)
}

func statelessGridImageURLs(item *scraper.InstaData, now time.Time) []string {
	if item == nil {
		return nil
	}
	urls := make([]string, 0, min(len(item.Medias), maxGridImages+1))
	for _, media := range item.Medias {
		raw := media.URL
		if media.IsVideo() {
			// Mixed carousels still need a visual tile for video slides. Use the
			// Instagram CDN poster; never download the MP4 just to build a grid.
			raw = media.ThumbnailURL
		}
		if !statelessMediaURLPlayable(raw, now) {
			continue
		}
		urls = append(urls, raw)
	}
	return urls
}
