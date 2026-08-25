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
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	compactAVCacheMaxAge     = 6 * time.Hour
	smartAVSourceMaxBytes    = 128 << 20
	smartAVTargetHeadroom    = 500_000
	smartAVMinVideoBitrate   = 450_000
	smartAVBackgroundTimeout = 90 * time.Second
)

var (
	compactAVMuxGroup    singleflight.Group
	smartAVBuildGroup    singleflight.Group
	smartAVTranscodeSlot = make(chan struct{}, 1)
	compactAVHTTPClient  = &http.Client{
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

	smartPath := compactAVSmartCachePath(postID)
	if compactAVCacheUsable(smartPath) {
		serveCompactAVFile(w, r, postID, smartPath, "smart")
		return
	}

	cachePath := compactAVCachePath(postID)
	if compactAVCacheUsable(cachePath) {
		serveCompactAVFile(w, r, postID, cachePath, "dash")
		triggerSmartAVBackground(postID, smartPath)
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
	triggerSmartAVBackground(postID, smartPath)
}

func compactAVCachePath(postID string) string {
	return filepath.Join(os.TempDir(), "instafix-compact-av", postID+".mp4")
}

func compactAVSmartCachePath(postID string) string {
	return filepath.Join(os.TempDir(), "instafix-compact-av", postID+"-smart.mp4")
}

// triggerSmartAVBackground upgrades the quick DASH remux only after the first
// preview has already been served. Telegram cancels media fetches that take too
// long, so the CPU-heavy transcode must never block an uncached first preview.
// Only one upgrade runs at a time on this VPS; when it is busy, later requests
// keep using the fast DASH fallback until another opportunity appears.
func triggerSmartAVBackground(postID, outputPath string) {
	if compactAVCacheUsable(outputPath) {
		return
	}
	select {
	case smartAVTranscodeSlot <- struct{}{}:
	default:
		return
	}

	go func() {
		defer func() { <-smartAVTranscodeSlot }()
		ctx, cancel := context.WithTimeout(context.Background(), smartAVBackgroundTimeout)
		defer cancel()
		_, err, _ := smartAVBuildGroup.Do(outputPath, func() (interface{}, error) {
			if compactAVCacheUsable(outputPath) {
				return outputPath, nil
			}
			if err := buildSmartAV(ctx, postID, outputPath); err != nil {
				return "", err
			}
			return outputPath, nil
		})
		if err != nil {
			slog.Info("background smart AV upgrade unavailable", "postID", postID, "err", err)
		}
	}()
}

func compactAVCacheUsable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > MaxInlineVideoBytes {
		return false
	}
	return time.Since(info.ModTime()) <= compactAVCacheMaxAge
}

// buildSmartAV lightly recompresses only an oversized progressive MP4. It
// keeps 720p-class resolution when the available bitrate is healthy, steps down
// to 540p/360p only for long clips, and falls back to the DASH remux on failure.
func buildSmartAV(ctx context.Context, postID, outputPath string) error {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return fmt.Errorf("ffmpeg unavailable: %w", err)
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		return fmt.Errorf("ffprobe unavailable: %w", err)
	}
	item, err := scraper.GetDataPreferVideoQuiet(postID)
	if err != nil || item == nil || len(item.Medias) == 0 || !item.Medias[0].IsVideo() {
		if err == nil {
			err = errors.New("progressive video metadata unavailable")
		}
		return err
	}
	originalURL := strings.TrimSpace(item.Medias[0].URL)
	if !scraper.IsAllowedMediaURL(originalURL) {
		return errors.New("progressive video URL is not on an allowed Instagram CDN host")
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o700); err != nil {
		return err
	}
	workDir, err := os.MkdirTemp(filepath.Dir(outputPath), "smart-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workDir)

	inputPath := filepath.Join(workDir, "input.mp4")
	if err := downloadCompactAVSource(ctx, originalURL, inputPath, smartAVSourceMaxBytes); err != nil {
		return fmt.Errorf("download progressive video: %w", err)
	}
	durationOut, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", inputPath).Output()
	if err != nil {
		return fmt.Errorf("ffprobe duration: %w", err)
	}
	duration, err := strconv.ParseFloat(strings.TrimSpace(string(durationOut)), 64)
	if err != nil || duration <= 0.1 {
		return errors.New("invalid progressive video duration")
	}
	targetBytes := MaxInlineVideoBytes - smartAVTargetHeadroom
	if targetBytes <= 0 {
		return errors.New("inline video limit too small for smart transcode")
	}
	// Telegram accepts fresh WebPage video documents through exactly 20 MiB.
	// Target 500 kB below the configured boundary because bitrate-based x264
	// output is not byte-exact. Reserve about 96 kbps for AAC + MP4 overhead;
	// the second encode attempt already reduces video bitrate another 8% if the
	// first result still lands above MaxInlineVideoBytes.
	totalBitrate := int64(float64(targetBytes*8) / duration)
	videoBitrate := totalBitrate - 96_000
	if videoBitrate < smartAVMinVideoBitrate {
		return fmt.Errorf("available video bitrate %d is too low for smart transcode", videoBitrate)
	}
	box := 1280
	if videoBitrate < 650_000 {
		box = 640
	} else if videoBitrate < 900_000 {
		box = 960
	}
	filter := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease:force_divisible_by=2", box, box)
	bitrates := []int64{videoBitrate, videoBitrate * 92 / 100}
	for attempt, bitrate := range bitrates {
		muxPath := filepath.Join(workDir, fmt.Sprintf("output-%d.mp4", attempt+1))
		cmd := exec.CommandContext(ctx, "ffmpeg",
			"-hide_banner", "-loglevel", "error", "-y",
			"-i", inputPath,
			"-map", "0:v:0",
			"-map", "0:a:0?",
			"-vf", filter,
			"-c:v", "libx264",
			"-preset", "veryfast",
			"-b:v", strconv.FormatInt(bitrate, 10),
			"-maxrate", strconv.FormatInt(bitrate*115/100, 10),
			"-bufsize", strconv.FormatInt(bitrate*2, 10),
			"-c:a", "copy",
			"-movflags", "+faststart",
			muxPath,
		)
		output, runErr := cmd.CombinedOutput()
		if runErr != nil {
			msg := strings.TrimSpace(string(output))
			if len(msg) > 1200 {
				msg = msg[:1200]
			}
			return fmt.Errorf("ffmpeg smart transcode: %w: %s", runErr, msg)
		}
		info, statErr := os.Stat(muxPath)
		if statErr != nil {
			return statErr
		}
		if info.Size() > 0 && info.Size() <= MaxInlineVideoBytes {
			if err := os.Rename(muxPath, outputPath); err != nil {
				return err
			}
			slog.Info("smart AV transcode cached", "postID", postID, "bytes", info.Size(), "target_bytes", targetBytes, "video_bitrate", bitrate, "box", box, "duration", duration)
			return nil
		}
	}
	return errors.New("smart transcode remained above inline video limit")
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
