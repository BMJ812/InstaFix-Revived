package handlers

import (
	"html/template"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

var playerTemplate = template.Must(template.New("player").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<style>html,body,video{width:100%;height:100%;margin:0;background:#000}video{object-fit:contain}</style>
</head>
<body><video controls autoplay playsinline src="{{.}}"></video></body>
</html>`))

// Player serves the HTML document required by twitter:player. The MP4 itself is
// delivered by Offload, which supports direct GET, HEAD, and Range responses.
func Player(w http.ResponseWriter, r *http.Request) {
	if rejectStatelessLegacyMedia(w) {
		return
	}
	postID := chi.URLParam(r, "postID")
	mediaNum, err := strconv.Atoi(chi.URLParam(r, "mediaNum"))
	if err != nil || mediaNum < 1 {
		http.Error(w, "invalid media number", http.StatusBadRequest)
		return
	}
	streamURL := requestPublicBaseURL(r) + "/offload/" + postID + "/" + strconv.Itoa(mediaNum) + ".mp4"
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; media-src https:; style-src 'unsafe-inline'; frame-ancestors *")
	if err := playerTemplate.Execute(w, streamURL); err != nil {
		http.Error(w, "player unavailable", http.StatusInternalServerError)
	}
}
