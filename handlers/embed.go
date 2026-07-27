package handlers

import (
	"errors"
	scraper "instafix/handlers/scraper"
	"instafix/utils"
	"instafix/views"
	"instafix/views/model"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

var MaxInlineVideoBytes int64

func mediaidToCode(mediaID int) string {
	alphabet := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	var shortCode string

	for mediaID > 0 {
		remainder := mediaID % 64
		mediaID /= 64
		shortCode = string(alphabet[remainder]) + shortCode
	}

	return shortCode
}

func getSharePostID(postID string) (string, error) {
	req, err := http.NewRequest("HEAD", "https://www.instagram.com/share/reel/"+postID+"/", nil)
	if err != nil {
		return postID, err
	}
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return postID, err
	}
	defer resp.Body.Close()
	redirURL, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		return postID, err
	}
	postID = path.Base(redirURL.Path)
	if postID == "login" {
		return postID, errors.New("not logged in")
	}
	return postID, nil
}

func Embed(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	viewsData := &model.ViewsData{}

	var err error
	postID := chi.URLParam(r, "postID")
	mediaNumParams := chi.URLParam(r, "mediaNum")
	urlQuery := r.URL.Query()
	if urlQuery == nil {
		return
	}
	if mediaNumParams == "" {
		imgIndex := urlQuery.Get("img_index")
		if imgIndex != "" {
			mediaNumParams = imgIndex
		} else {
			mediaNumParams = "0"
		}
	}
	mediaNum, err := strconv.Atoi(mediaNumParams)
	if err != nil {
		viewsData.Description = "Invalid img_index parameter"
		views.Embed(viewsData, w)
		return
	}

	isDirect, _ := strconv.ParseBool(urlQuery.Get("direct"))
	isGallery, _ := strconv.ParseBool(urlQuery.Get("gallery"))

	// Get direct/gallery from header too, nginx query params is pain in the ass
	embedType := r.Header.Get("X-Embed-Type")
	if embedType == "direct" {
		isDirect = true
	} else if embedType == "gallery" {
		isGallery = true
	}

	// Stories use mediaID (int) instead of postID
	if strings.Contains(r.URL.Path, "/stories/") {
		mediaID, err := strconv.Atoi(postID)
		if err != nil {
			viewsData.Description = "Invalid postID"
			views.Embed(viewsData, w)
			return
		}
		postID = mediaidToCode(mediaID)
	} else if strings.Contains(r.URL.Path, "/share/") {
		postID, err = getSharePostID(postID)
		if err != nil && len(scraper.RemoteScraperAddr) == 0 {
			slog.Error("Failed to get new postID from share URL", "postID", postID, "err", err)
			viewsData.Description = "Failed to get new postID from share URL"
			views.Embed(viewsData, w)
			return
		}
	}

	// If User-Agent is not bot, redirect to Instagram
	viewsData.Title = "Instagram preview"
	viewsData.URL = "https://instagram.com" + strings.Replace(r.URL.RequestURI(), "/"+mediaNumParams, "", 1)
	viewsData.CanonicalURL = viewsData.URL
	viewsData.Site = "Instagram preview"
	if !utils.IsBot(r.Header.Get("User-Agent")) {
		http.Redirect(w, r, viewsData.URL, http.StatusFound)
		return
	}

	preferVideo := strings.Contains(r.URL.Path, "/reel/") || strings.Contains(r.URL.Path, "/reels/") || strings.Contains(r.URL.Path, "/tv/")
	var item *scraper.InstaData
	if preferVideo {
		item, err = scraper.GetDataPreferVideoQuiet(postID)
	} else {
		item, err = scraper.GetDataQuiet(postID)
	}
	needsAuthFallback := err != nil || item == nil || len(item.Medias) == 0 || len(item.Username) == 0 ||
		preferVideo && item != nil && !item.HasVideo()
	var authFallbackErr error
	if needsAuthFallback {
		if authItem, authErr := scraper.GetDataEmbedAuthFallback(postID); authErr == nil && authItem != nil && len(authItem.Medias) > 0 {
			if shouldUseAuthFallbackItem(item, authItem, preferVideo) {
				item = authItem
			}
			err = nil
		} else if authErr != nil {
			authFallbackErr = authErr
			slog.Info("Embed auth fallback unavailable", "postID", postID, "err", authErr)
		}
	}
	if err != nil || item == nil || len(item.Medias) == 0 {
		if err == nil {
			err = authFallbackErr
		}
		renderFallbackEmbed(w, r, viewsData, postID, err)
		return
	}

	if mediaNum > len(item.Medias) {
		viewsData.Description = "Media number out of range"
		views.Embed(viewsData, w)
		return
	} else if len(item.Username) == 0 {
		renderFallbackEmbed(w, r, viewsData, postID, scraper.ErrNotFound)
		return
	}

	var sb strings.Builder
	sb.Grow(32) // 32 bytes should be enough for most cases
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	publicBaseURL := scheme + "://" + r.Host

	viewsData.Title = "@" + item.Username
	viewsData.Creator = "@" + item.Username
	// Gallery do not have any caption
	if !isGallery {
		viewsData.Description = embedDescription(item)
	}

	media := item.Medias[max(1, mediaNum)-1]
	isImage := media.IsImage()
	switch {
	case mediaNum == 0 && isImage && len(item.Medias) > 1:
		viewsData.Card = "summary_large_image"
		viewsData.OGType = "article"
		sb.WriteString(publicBaseURL + "/grid/")
		sb.WriteString(postID)
		viewsData.ImageURL = sb.String()
		viewsData.ImageURLs = carouselImageURLs(publicBaseURL, postID, item.Medias, 3)
		if isDirect {
			sb.Reset()
			sb.WriteString(publicBaseURL + "/offload/")
			sb.WriteString(postID)
			sb.WriteString("/1")
		}
	case isImage:
		viewsData.Card = "summary_large_image"
		viewsData.OGType = "article"
		sb.WriteString(publicBaseURL + "/offload/")
		sb.WriteString(postID)
		sb.WriteString("/")
		sb.WriteString(strconv.Itoa(max(1, mediaNum)))
		viewsData.ImageURL = sb.String()
	default:
		videoRoute := previewVideoRoute(publicBaseURL, postID, max(1, mediaNum), r.UserAgent())
		// Telegram treats Twitter player cards as an iframe-style preview and
		// falls back to the thumbnail without fetching og:video. Keep the
		// Twitter card image-compatible and expose the MP4 through Open Graph,
		// matching the metadata contract used by OGInstagram.
		viewsData.Card = "summary_large_image"
		viewsData.OGType = "article"
		sb.WriteString(videoRoute)
		viewsData.Width, viewsData.Height = videoDisplaySize(media.Width, media.Height)
		if viewsData.Width <= 0 {
			viewsData.Width = 400
		}
		if viewsData.Height <= 0 {
			viewsData.Height = 400
		}
		if media.ThumbnailURL != "" {
			viewsData.ImageURL = publicBaseURL + "/offload/" + postID + "/" + strconv.Itoa(max(1, mediaNum)) + "?thumbnail=1"
			viewsData.ImageWidth = media.Width
			viewsData.ImageHeight = media.Height
			viewsData.ImageAlt = strings.ReplaceAll(strings.TrimSpace(item.Caption), "\n", " ")
		}
		viewsData.VideoURL = videoRoute

		viewsData.OEmbedURL = scheme + "://" + r.Host + "/oembed?text=" + url.QueryEscape(viewsData.Description) + "&url=" + viewsData.URL
	}
	if isDirect {
		http.Redirect(w, r, sb.String(), http.StatusFound)
		return
	}

	views.Embed(viewsData, w)
}

func previewVideoRoute(publicBaseURL, postID string, mediaNum int, userAgent string) string {
	route := publicBaseURL + "/offload/" + postID + "/" + strconv.Itoa(mediaNum)
	if shouldRedirectPreviewVideo(userAgent) {
		route += "?delivery=cdn-redirect-v3"
	}
	return route
}

func videoDisplaySize(width, height int) (int, int) {
	if width > 0 && height > 0 && width < 400 && height < 400 {
		return width * 2, height * 2
	}
	if width <= 0 || height <= 0 {
		return width, height
	}
	const maxPreviewDimension = 1080
	multiplier := 1.0
	if width > height && width > maxPreviewDimension {
		multiplier = float64(maxPreviewDimension) / float64(width)
	} else if height >= width && height > maxPreviewDimension {
		multiplier = float64(maxPreviewDimension) / float64(height)
	}
	return int(float64(width)*multiplier + 0.5), int(float64(height)*multiplier + 0.5)
}

func shouldUseAuthFallbackItem(current, auth *scraper.InstaData, preferVideo bool) bool {
	if auth == nil || len(auth.Medias) == 0 {
		return false
	}
	if current == nil || len(current.Medias) == 0 || current.Username == "" {
		return true
	}
	if preferVideo {
		return auth.HasVideo()
	}
	return true
}

func embedDescription(item *scraper.InstaData) string {
	if item == nil {
		return ""
	}
	stats := mediaStatsLine(item)
	caption := strings.TrimSpace(item.Caption)
	description := caption
	if stats != "" && caption != "" {
		description = stats + "\n\n" + caption
	} else if stats != "" {
		description = stats
	}
	if len(description) > 255 {
		description = utils.Substr(description, 0, 250) + "..."
	}
	return description
}

func mediaStatsLine(item *scraper.InstaData) string {
	if item == nil {
		return ""
	}
	parts := make([]string, 0, 3)
	if item.HasViewCount {
		parts = append(parts, "▶️ "+formatStatCount(item.ViewCount))
	}
	if item.HasLikeCount {
		parts = append(parts, "❤️ "+formatStatCount(item.LikeCount))
	}
	if item.HasCommentCount {
		parts = append(parts, "💬 "+formatStatCount(item.CommentCount))
	}
	return strings.Join(parts, "  ")
}

func formatStatCount(value int64) string {
	if value < 0 {
		value = 0
	}
	raw := strconv.FormatInt(value, 10)
	if len(raw) <= 3 {
		return raw
	}
	var formatted strings.Builder
	prefix := len(raw) % 3
	if prefix > 0 {
		formatted.WriteString(raw[:prefix])
	}
	for i := prefix; i < len(raw); i += 3 {
		if formatted.Len() > 0 {
			formatted.WriteByte(',')
		}
		formatted.WriteString(raw[i : i+3])
	}
	return formatted.String()
}

func isTelegramBot(userAgent string) bool {
	return strings.Contains(strings.ToLower(userAgent), "telegrambot")
}

func ConfigureMaxInlineVideoBytes(maxBytes int64) {
	if maxBytes >= 0 {
		MaxInlineVideoBytes = maxBytes
	}
}

func isInlineVideoOversized(videoURL string) bool {
	if MaxInlineVideoBytes <= 0 || strings.TrimSpace(videoURL) == "" {
		return false
	}
	req, err := http.NewRequest(http.MethodHead, videoURL, http.NoBody)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", "Instagram 273.0.0.16.70 (iPhone15,2; iOS 17_5_1; en_US; en-US; scale=3.00; 1290x2796; 470085518)")
	client := http.Client{Timeout: 4 * time.Second}
	res, err := client.Do(req)
	if err != nil || res == nil {
		return false
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 || res.ContentLength <= 0 {
		return false
	}
	if res.ContentLength > MaxInlineVideoBytes {
		slog.Info("inline video disabled: oversized", "contentLength", res.ContentLength, "maxBytes", MaxInlineVideoBytes, "host", safeURLHost(videoURL))
		return true
	}
	return false
}

func safeURLHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "invalid"
	}
	return u.Host
}

func carouselImageURLs(publicBaseURL, postID string, medias []scraper.Media, limit int) []string {
	if limit <= 0 {
		return nil
	}
	urls := make([]string, 0, limit)
	for idx, media := range medias {
		if len(urls) >= limit {
			break
		}
		if !media.IsImage() && media.ThumbnailURL == "" {
			continue
		}
		mediaURL := publicBaseURL + "/offload/" + postID + "/" + strconv.Itoa(idx+1)
		if !media.IsImage() {
			mediaURL += "?thumbnail=1"
		}
		urls = append(urls, mediaURL)
	}
	return urls
}

func renderFallbackEmbed(w http.ResponseWriter, r *http.Request, viewsData *model.ViewsData, postID string, scrapeErr error) {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	publicBaseURL := scheme + "://" + r.Host
	viewsData.Title = "Oops, preview unavailable"
	viewsData.Creator = "@instagram"
	viewsData.Card = "summary_large_image"
	viewsData.OGType = "article"
	viewsData.Description = "Instagram did not provide public media for this post. Open it on Instagram to view the original."
	viewsData.ImageURL = publicBaseURL + "/fallback/" + postID + ".png"
	if scrapeErr != nil {
		slog.Info("Serving generic fallback preview", "postID", postID, "err", scrapeErr)
	}
	views.Embed(viewsData, w)
}
