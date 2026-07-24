package handlers

import (
	"errors"
	"fmt"
	scraper "instafix/handlers/scraper"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

var (
	offloadGetDataPreferVideo     = scraper.GetDataPreferVideo
	offloadRefreshDataPreferVideo = scraper.RefreshDataPreferVideo
	offloadMediaURLAllowed        = scraper.IsAllowedMediaURL
	offloadVideoClient            = &http.Client{
		Transport: previewVideoProxyClient.Transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("video redirect limit exceeded")
			}
			if !scraper.IsAllowedMediaURL(req.URL.String()) {
				return errors.New("video redirect left allowed Instagram CDN hosts")
			}
			return nil
		},
	}
)

// Offload resolves a stable local media URL to the current Instagram CDN URL.
// Images retain the inexpensive redirect behavior. Videos are streamed so
// preview clients never receive a redirect as their MP4 target.
func Offload(w http.ResponseWriter, r *http.Request) {
	postID := chi.URLParam(r, "postID")
	mediaNum, err := strconv.Atoi(chi.URLParam(r, "mediaNum"))
	if err != nil || mediaNum < 1 {
		http.Error(w, "invalid media number", http.StatusBadRequest)
		return
	}

	item, err := offloadGetDataPreferVideo(postID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if mediaNum > len(item.Medias) {
		http.Error(w, "media number out of range", http.StatusNotFound)
		return
	}

	media := item.Medias[mediaNum-1]
	target := media.URL
	if r.URL.Query().Has("thumbnail") && media.ThumbnailURL != "" {
		target = media.ThumbnailURL
	}
	if target == "" {
		http.Error(w, "media URL unavailable", http.StatusNotFound)
		return
	}
	if !r.URL.Query().Has("thumbnail") && media.IsVideo() {
		streamOffloadVideo(w, r, postID, mediaNum, target)
		return
	}

	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Location", target)
	w.WriteHeader(http.StatusFound)
}

func streamOffloadVideo(w http.ResponseWriter, r *http.Request, postID string, mediaNum int, videoURL string) {
	res, err := requestVideoResponse(r, videoURL)
	if err != nil {
		slog.Info("offload video URL needs refresh", "postID", postID, "err", err)
		refreshed, refreshErr := offloadRefreshDataPreferVideo(postID)
		if refreshErr == nil && mediaNum <= len(refreshed.Medias) {
			media := refreshed.Medias[mediaNum-1]
			if media.IsVideo() && media.URL != "" && media.URL != videoURL {
				res, err = requestVideoResponse(r, media.URL)
			} else {
				err = errors.New("refreshed media did not contain a new video URL")
			}
		} else if refreshErr != nil {
			err = fmt.Errorf("refresh failed: %w", refreshErr)
		}
	}
	if err != nil || res == nil {
		slog.Warn("offload video unavailable after refresh", "postID", postID, "err", err)
		http.Error(w, "video stream temporarily unavailable", http.StatusBadGateway)
		return
	}
	defer res.Body.Close()

	copyVideoResponseHeaders(w.Header(), res.Header)
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "video/mp4")
	}
	if w.Header().Get("Accept-Ranges") == "" {
		w.Header().Set("Accept-Ranges", "bytes")
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("X-InstaFix-Video-Stream", "direct")
	w.WriteHeader(res.StatusCode)
	if r.Method == http.MethodHead {
		slog.Info("offload video HEAD served", "postID", postID, "status", res.StatusCode)
		return
	}

	written, copyErr := io.CopyBuffer(w, res.Body, make([]byte, 64*1024))
	if copyErr != nil {
		slog.Warn("offload video client copy failed", "postID", postID, "bytes", written, "err", copyErr)
		return
	}
	slog.Info("offload video streamed", "postID", postID, "status", res.StatusCode, "bytes", written, "range", r.Header.Get("Range") != "")
}

type videoUpstreamError struct {
	status int
}

func (e *videoUpstreamError) Error() string {
	return "video upstream returned " + strconv.Itoa(e.status)
}

func requestVideoResponse(r *http.Request, videoURL string) (*http.Response, error) {
	res, err := doVideoRequest(r, r.Method, videoURL, r.Header.Get("Range"))
	if r.Method != http.MethodHead || usableVideoResponse(res, err) {
		return res, responseError(res, err)
	}
	if res != nil {
		res.Body.Close()
	}

	probeRange := r.Header.Get("Range")
	if probeRange == "" {
		probeRange = "bytes=0-0"
	}
	probe, probeErr := doVideoRequest(r, http.MethodGet, videoURL, probeRange)
	return probe, responseError(probe, probeErr)
}

func doVideoRequest(r *http.Request, method, videoURL, rangeHeader string) (*http.Response, error) {
	if !offloadMediaURLAllowed(videoURL) {
		return nil, errors.New("video URL host is not allowed")
	}
	req, err := http.NewRequestWithContext(r.Context(), method, videoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Instagram 273.0.0.16.70 (iPhone15,2; iOS 17_5_1; en_US; en-US; scale=3.00; 1290x2796; 470085518)")
	req.Header.Set("Accept", "video/mp4,video/*;q=0.9,*/*;q=0.1")
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	return offloadVideoClient.Do(req)
}

func responseError(res *http.Response, err error) error {
	if err != nil {
		return err
	}
	if res == nil {
		return http.ErrAbortHandler
	}
	if !usableVideoResponse(res, nil) {
		res.Body.Close()
		return &videoUpstreamError{status: res.StatusCode}
	}
	return nil
}

func usableVideoResponse(res *http.Response, err error) bool {
	if err != nil || res == nil {
		return false
	}
	if res.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		return true
	}
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusPartialContent {
		return false
	}
	contentType := strings.ToLower(strings.TrimSpace(res.Header.Get("Content-Type")))
	return !strings.HasPrefix(contentType, "text/") &&
		!strings.Contains(contentType, "application/json")
}

func copyVideoResponseHeaders(dst, src http.Header) {
	for _, key := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified", "ETag", "Cache-Control"} {
		if value := src.Get(key); value != "" {
			dst.Set(key, value)
		}
	}
}
