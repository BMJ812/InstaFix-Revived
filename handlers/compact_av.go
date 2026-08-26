package handlers

import (
	"context"
	"errors"
	"fmt"
	scraper "instafix/handlers/scraper"
	"instafix/observability"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"
)

const compactAVCacheMaxAge = 6 * time.Hour

var (
	compactAVMuxGroup   singleflight.Group
	compactAVHTTPClient = &http.Client{
		Timeout: 25 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("compact media redirect limit exceeded")
			}
			if !scraper.IsAllowedMediaURL(req.URL.String()) {
				return errors.New("compact media redirect left Instagram CDN")
			}
			return nil
		},
	}
)

// ServeCompactAV is a narrow oversized-Reel fallback for Telegram. Instagram
// exposes smaller DASH video and audio tracks separately; this remuxes them
// without re-encoding, caches the result on ephemeral disk, and serves a normal
// range-capable MP4. Small videos still use the zero-bandwidth CDN redirect path.
func ServeCompactAV(w http.ResponseWriter, r *http.Request, postID string) {
	if MaxInlineVideoBytes <= 0 {
		if isTelegramBot(r.UserAgent()) {
			observability.Default.RecordTelegramVideoDelivery(postID, "failed", 0, "compact_disabled")
		}
		http.Error(w, "compact video disabled", http.StatusNotFound)
		return
	}

	cachePath := compactAVCachePath(postID)
	if compactAVCacheUsable(cachePath) {
		serveCompactAVFile(w, r, postID, cachePath, "dash")
		return
	}

	pathValue, err, _ := compactAVMuxGroup.Do(cachePath, func() (interface{}, error) {
		if compactAVCacheUsable(cachePath) {
			return cachePath, nil
		}
		sources, err := scraper.ResolveCompactPreviewAV(postID, MaxInlineVideoBytes)
		if err != nil {
			return "", fmt.Errorf("resolve compact AV sources: %w", err)
		}
		if err := buildCompactAV(r.Context(), cachePath, sources); err != nil {
			return "", err
		}
		return cachePath, nil
	})
	if err != nil {
		slog.Warn("compact AV mux failed", "postID", postID, "err", err)
		if isTelegramBot(r.UserAgent()) {
			observability.Default.RecordTelegramVideoDelivery(postID, "failed", 0, "compact_mux_failed")
		}
		http.Error(w, "compact video temporarily unavailable", http.StatusBadGateway)
		return
	}
	path, ok := pathValue.(string)
	if !ok || path == "" {
		if isTelegramBot(r.UserAgent()) {
			observability.Default.RecordTelegramVideoDelivery(postID, "failed", 0, "compact_result_missing")
		}
		http.Error(w, "compact video temporarily unavailable", http.StatusBadGateway)
		return
	}
	serveCompactAVFile(w, r, postID, path, "dash")
}

func compactAVCachePath(postID string) string {
	return filepath.Join(os.TempDir(), "instafix-compact-av", postID+".mp4")
}

func compactAVCacheUsable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > MaxInlineVideoBytes {
		return false
	}
	return time.Since(info.ModTime()) <= compactAVCacheMaxAge
}

func buildCompactAV(ctx context.Context, outputPath string, sources scraper.CompactPreviewSources) error {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return fmt.Errorf("ffmpeg unavailable: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o700); err != nil {
		return err
	}
	workDir, err := os.MkdirTemp(filepath.Dir(outputPath), "mux-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workDir)

	videoPath := filepath.Join(workDir, "video.mp4")
	audioPath := filepath.Join(workDir, "audio.mp4")
	muxPath := filepath.Join(workDir, "output.mp4")
	if err := downloadCompactAVSource(ctx, sources.Video.URL, videoPath, MaxInlineVideoBytes); err != nil {
		return fmt.Errorf("download compact video: %w", err)
	}
	if err := downloadCompactAVSource(ctx, sources.AudioURL, audioPath, MaxInlineVideoBytes); err != nil {
		return fmt.Errorf("download compact audio: %w", err)
	}

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error", "-y",
		"-i", videoPath,
		"-i", audioPath,
		"-map", "0:v:0",
		"-map", "1:a:0",
		"-c", "copy",
		"-shortest",
		"-movflags", "+faststart",
		muxPath,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(output))
		if len(msg) > 1200 {
			msg = msg[:1200]
		}
		return fmt.Errorf("ffmpeg remux: %w: %s", err, msg)
	}
	info, err := os.Stat(muxPath)
	if err != nil {
		return err
	}
	if info.Size() <= 0 || info.Size() > MaxInlineVideoBytes {
		return fmt.Errorf("remuxed MP4 size %d exceeds limit %d", info.Size(), MaxInlineVideoBytes)
	}
	if err := os.Rename(muxPath, outputPath); err != nil {
		return err
	}
	slog.Info("compact AV mux cached",
		"bytes", info.Size(),
		"video_bytes", sources.VideoBytes,
		"audio_bytes", sources.AudioBytes,
		"width", sources.Video.Width,
		"height", sources.Video.Height,
	)
	return nil
}

func downloadCompactAVSource(ctx context.Context, rawURL, dest string, maxBytes int64) error {
	if !scraper.IsAllowedMediaURL(rawURL) {
		return errors.New("media URL is not on an allowed Instagram CDN host")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Instagram 273.0.0.16.70 (iPhone15,2; iOS 17_5_1; en_US; en-US; scale=3.00; 1290x2796; 470085518)")
	res, err := compactAVHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("Instagram CDN HTTP %d", res.StatusCode)
	}
	if res.ContentLength > maxBytes {
		return fmt.Errorf("source content-length %d exceeds limit %d", res.ContentLength, maxBytes)
	}
	file, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	limited := io.LimitReader(res.Body, maxBytes+1)
	written, copyErr := io.Copy(file, limited)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written <= 0 || written > maxBytes {
		return fmt.Errorf("downloaded source size %d exceeds limit %d", written, maxBytes)
	}
	return nil
}

func serveCompactAVFile(w http.ResponseWriter, r *http.Request, postID, path, delivery string) {
	file, err := os.Open(path)
	if err != nil {
		if isTelegramBot(r.UserAgent()) {
			observability.Default.RecordTelegramVideoDelivery(postID, "failed", 0, "compact_file_open_failed")
		}
		http.Error(w, "compact video unavailable", http.StatusBadGateway)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		if isTelegramBot(r.UserAgent()) {
			observability.Default.RecordTelegramVideoDelivery(postID, "failed", 0, "compact_file_stat_failed")
		}
		http.Error(w, "compact video unavailable", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("X-Instagram7-Experiment", "stateless-azure")
	w.Header().Set("X-InstaFix-Video-Delivery", "stateless-compact-av-"+delivery)
	slog.Info("compact AV served", "postID", postID, "bytes", info.Size(), "delivery", delivery, "method", r.Method, "range", r.Header.Get("Range") != "")
	if isTelegramBot(r.UserAgent()) && r.Method == http.MethodGet {
		observability.Default.RecordTelegramVideoDelivery(postID, delivery, info.Size(), "")
	}
	http.ServeContent(w, r, "video.mp4", info.ModTime(), file)
}
