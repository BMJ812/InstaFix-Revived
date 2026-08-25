package handlers

import (
	"errors"
	scraper "instafix/handlers/scraper"
	"instafix/observability"
	"instafix/utils"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
)

const (
	ogInstagramClientRedirectModeEnv             = "OGINSTAGRAM_CLIENT_REDIRECT_MODE"
	legacyOGInstagramRestrictedClientRedirectEnv = "OGINSTAGRAM_RESTRICTED_CLIENT_REDIRECT"
	ogInstagramClientRedirectTelegramRestricted  = "telegram_restricted"
	ogInstagramClientRedirectTelegramAll         = "telegram_all"
	ogInstagramClientRedirectBotsRestricted      = "bots_restricted"
	ogInstagramClientRedirectBotsAll             = "bots_all"
	ogInstagramClientRedirectPreviewFallback     = "preview_fallback"
)

func TryOGInstagramClientRedirect(w http.ResponseWriter, r *http.Request, postID string, scrapeErr error) bool {
	mode := ogInstagramClientRedirectMode()
	if !isPublicationPath(r.URL.Path) {
		return false
	}
	switch mode {
	case ogInstagramClientRedirectPreviewFallback:
		if scrapeErr == nil || !isPreviewMediaBot(r.UserAgent()) {
			return false
		}
	case ogInstagramClientRedirectTelegramAll:
		if !isTelegramBot(r.UserAgent()) {
			return false
		}
	case ogInstagramClientRedirectBotsAll:
		if !utils.IsBot(r.UserAgent()) {
			return false
		}
	case ogInstagramClientRedirectBotsRestricted:
		if !utils.IsBot(r.UserAgent()) || !isVideoPublicationPath(r.URL.Path) || !errors.Is(scrapeErr, scraper.ErrRestricted) {
			return false
		}
	case ogInstagramClientRedirectTelegramRestricted:
		if !isTelegramBot(r.UserAgent()) || !isVideoPublicationPath(r.URL.Path) || !errors.Is(scrapeErr, scraper.ErrRestricted) {
			return false
		}
	default:
		return false
	}

	cfg := loadOGInstagramProxyConfig()
	base, err := url.Parse(cfg.upstream)
	if err != nil || base.Scheme != "https" || base.Host == "" {
		slog.Warn("client redirect skipped: invalid upstream", "postID", postID)
		return false
	}
	publicationPath := "reels"
	if strings.Contains(r.URL.Path, "/p/") {
		publicationPath = "p"
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/" + publicationPath + "/" + url.PathEscape(postID) + "/"
	base.RawPath = ""
	base.RawQuery = ""
	base.Fragment = ""

	w.Header().Set("Cache-Control", "no-store")
	observability.Default.RecordOGClientRedirect(r, postID, mode)
	slog.Warn("preview client redirected to OGInstagram",
		"path", r.URL.Path,
		"postID", postID,
		"mode", mode,
		"upstreamHost", base.Host,
	)
	http.Redirect(w, r, base.String(), http.StatusFound)
	return true
}

func ogInstagramClientRedirectMode() string {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv(ogInstagramClientRedirectModeEnv)))
	switch mode {
	case ogInstagramClientRedirectTelegramRestricted, ogInstagramClientRedirectTelegramAll, ogInstagramClientRedirectBotsRestricted, ogInstagramClientRedirectBotsAll, ogInstagramClientRedirectPreviewFallback:
		return mode
	case "", "off":
	default:
		return "off"
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv(legacyOGInstagramRestrictedClientRedirectEnv)), "telegram") {
		return ogInstagramClientRedirectTelegramRestricted
	}
	return "off"
}

func isPublicationPath(requestPath string) bool {
	return isVideoPublicationPath(requestPath) || strings.Contains(requestPath, "/p/")
}

func isVideoPublicationPath(requestPath string) bool {
	return strings.Contains(requestPath, "/reel/") ||
		strings.Contains(requestPath, "/reels/") ||
		strings.Contains(requestPath, "/tv/")
}
