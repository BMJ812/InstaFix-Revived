package main

import (
	"bytes"
	_ "embed"
	"net/http"
	"time"
)

//go:embed video/instagram7-reel/out/instagram7-test-reel.mp4
var demoReelMP4 []byte

//go:embed video/instagram7-reel/out/instagram7-test-reel-poster.webp
var demoReelPosterWebP []byte

var demoReelModified = time.Date(2026, time.August, 8, 0, 0, 0, 0, time.UTC)

const faviconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">
<defs><linearGradient id="g" x1="7" y1="57" x2="57" y2="7" gradientUnits="userSpaceOnUse"><stop stop-color="#ffb000"/><stop offset=".42" stop-color="#ff2768"/><stop offset="1" stop-color="#7b3cff"/></linearGradient></defs>
<rect width="64" height="64" rx="14" fill="url(#g)"/>
<rect x="14" y="14" width="36" height="36" rx="11" fill="none" stroke="#fff" stroke-width="4"/>
<circle cx="32" cy="32" r="9" fill="none" stroke="#fff" stroke-width="4"/>
<circle cx="44" cy="20" r="3" fill="#fff"/>
<path d="M39 37h7v4h-3v9h-4z" fill="#fff"/>
</svg>`

func serveFavicon(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=604800, immutable")
	w.Header().Set("Cloudflare-CDN-Cache-Control", "public, max-age=604800")
	_, _ = w.Write([]byte(faviconSVG))
}

func serveDemoReelVideo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "public, max-age=604800, immutable")
	w.Header().Set("Cloudflare-CDN-Cache-Control", "public, max-age=604800")
	http.ServeContent(w, r, "instagram7-test-reel.mp4", demoReelModified, bytes.NewReader(demoReelMP4))
}

func serveDemoReelPoster(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/webp")
	w.Header().Set("Cache-Control", "public, max-age=604800, immutable")
	w.Header().Set("Cloudflare-CDN-Cache-Control", "public, max-age=604800")
	http.ServeContent(w, r, "instagram7-test-reel-poster.webp", demoReelModified, bytes.NewReader(demoReelPosterWebP))
}
