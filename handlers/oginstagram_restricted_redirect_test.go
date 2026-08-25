package handlers

import (
	"errors"
	scraper "instafix/handlers/scraper"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRestrictedClientRedirectRequiresExplicitTelegramMode(t *testing.T) {
	t.Setenv(ogInstagramClientRedirectModeEnv, "")
	t.Setenv(legacyOGInstagramRestrictedClientRedirectEnv, "")
	t.Setenv("OGINSTAGRAM_PROXY_UPSTREAM", "https://og.example")

	request := httptest.NewRequest(http.MethodGet, "https://instagram7.test/reels/DaRestricted/", nil)
	request.Header.Set("User-Agent", "TelegramBot (like TwitterBot)")
	recorder := httptest.NewRecorder()

	if TryOGInstagramClientRedirect(recorder, request, "DaRestricted", scraper.ErrRestricted) {
		t.Fatal("redirect enabled without explicit mode")
	}
}

func TestRestrictedClientRedirectOnlyHandlesRestrictedTelegramReels(t *testing.T) {
	t.Setenv(ogInstagramClientRedirectModeEnv, ogInstagramClientRedirectTelegramRestricted)
	t.Setenv("OGINSTAGRAM_PROXY_UPSTREAM", "https://og.example")

	tests := []struct {
		name      string
		target    string
		userAgent string
		scrapeErr error
	}{
		{
			name:      "browser",
			target:    "https://instagram7.test/reels/DaRestricted/",
			userAgent: "Mozilla/5.0",
			scrapeErr: scraper.ErrRestricted,
		},
		{
			name:      "image post",
			target:    "https://instagram7.test/p/DaRestricted/",
			userAgent: "TelegramBot (like TwitterBot)",
			scrapeErr: scraper.ErrRestricted,
		},
		{
			name:      "not found",
			target:    "https://instagram7.test/reels/DaRestricted/",
			userAgent: "TelegramBot (like TwitterBot)",
			scrapeErr: scraper.ErrNotFound,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.target, nil)
			request.Header.Set("User-Agent", test.userAgent)
			recorder := httptest.NewRecorder()
			if TryOGInstagramClientRedirect(recorder, request, "DaRestricted", test.scrapeErr) {
				t.Fatal("unexpected redirect")
			}
		})
	}
}

func TestRestrictedClientRedirectSendsTelegramToOGInstagram(t *testing.T) {
	t.Setenv(ogInstagramClientRedirectModeEnv, ogInstagramClientRedirectTelegramRestricted)
	t.Setenv("OGINSTAGRAM_PROXY_UPSTREAM", "https://og.example/base/")

	request := httptest.NewRequest(http.MethodGet, "https://instagram7.test/reels/DaRestricted/?cache=bust", nil)
	request.Header.Set("User-Agent", "TelegramBot (like TwitterBot)")
	recorder := httptest.NewRecorder()
	restricted := errors.Join(errors.New("public scrape failed"), scraper.ErrRestricted)

	if !TryOGInstagramClientRedirect(recorder, request, "DaRestricted", restricted) {
		t.Fatal("expected restricted Telegram Reel redirect")
	}
	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusFound)
	}
	if location := recorder.Header().Get("Location"); location != "https://og.example/base/reels/DaRestricted/" {
		t.Fatalf("Location = %q", location)
	}
	if cacheControl := recorder.Header().Get("Cache-Control"); cacheControl != "no-store" {
		t.Fatalf("Cache-Control = %q", cacheControl)
	}
}

func TestClientRedirectTelegramAllHandlesPostsWithoutScraping(t *testing.T) {
	t.Setenv(ogInstagramClientRedirectModeEnv, ogInstagramClientRedirectTelegramAll)
	t.Setenv("OGINSTAGRAM_PROXY_UPSTREAM", "https://og.example")

	tests := []struct {
		requestPath string
		location    string
	}{
		{"https://instagram7.test/reels/DaVideo/", "https://og.example/reels/DaPublication/"},
		{"https://instagram7.test/p/DaImage/", "https://og.example/p/DaPublication/"},
	}
	for _, test := range tests {
		request := httptest.NewRequest(http.MethodGet, test.requestPath, nil)
		request.Header.Set("User-Agent", "TelegramBot (like TwitterBot)")
		recorder := httptest.NewRecorder()
		if !TryOGInstagramClientRedirect(recorder, request, "DaPublication", nil) {
			t.Fatalf("expected telegram_all redirect for %s", test.requestPath)
		}
		if recorder.Code != http.StatusFound {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusFound)
		}
		if location := recorder.Header().Get("Location"); location != test.location {
			t.Fatalf("Location = %q, want %q", location, test.location)
		}
	}
}

func TestClientRedirectBotsAllHandlesKnownPreviewBots(t *testing.T) {
	t.Setenv(ogInstagramClientRedirectModeEnv, ogInstagramClientRedirectBotsAll)
	t.Setenv("OGINSTAGRAM_PROXY_UPSTREAM", "https://og.example")

	for _, userAgent := range []string{
		"TelegramBot (like TwitterBot)",
		"Mozilla/5.0 (compatible; Discordbot/2.0)",
		"WhatsApp/2.0",
		"Slackbot-LinkExpanding 1.0",
	} {
		request := httptest.NewRequest(http.MethodGet, "https://instagram7.test/reels/DaVideo/", nil)
		request.Header.Set("User-Agent", userAgent)
		recorder := httptest.NewRecorder()
		if !TryOGInstagramClientRedirect(recorder, request, "DaVideo", nil) {
			t.Fatalf("expected bots_all redirect for %q", userAgent)
		}
	}

	request := httptest.NewRequest(http.MethodGet, "https://instagram7.test/reels/DaVideo/", nil)
	request.Header.Set("User-Agent", "Mozilla/5.0")
	recorder := httptest.NewRecorder()
	if TryOGInstagramClientRedirect(recorder, request, "DaVideo", nil) {
		t.Fatal("bots_all redirected a browser")
	}
}

func TestClientRedirectBotsRestrictedOnlyHandlesRestrictedVideoPreviews(t *testing.T) {
	t.Setenv(ogInstagramClientRedirectModeEnv, ogInstagramClientRedirectBotsRestricted)
	t.Setenv("OGINSTAGRAM_PROXY_UPSTREAM", "https://og.example")

	for _, userAgent := range []string{
		"TelegramBot (like TwitterBot)",
		"Mozilla/5.0 (compatible; Discordbot/2.0)",
		"WhatsApp/2.0",
		"Slackbot-LinkExpanding 1.0",
	} {
		request := httptest.NewRequest(http.MethodGet, "https://instagram7.test/reels/DaRestricted/", nil)
		request.Header.Set("User-Agent", userAgent)
		recorder := httptest.NewRecorder()
		if !TryOGInstagramClientRedirect(recorder, request, "DaRestricted", scraper.ErrRestricted) {
			t.Fatalf("expected bots_restricted redirect for %q", userAgent)
		}
	}

	tests := []struct {
		name      string
		target    string
		userAgent string
		scrapeErr error
	}{
		{"successful Reel", "https://instagram7.test/reels/DaVideo/", "Discordbot/2.0", nil},
		{"not found Reel", "https://instagram7.test/reels/DaMissing/", "Discordbot/2.0", scraper.ErrNotFound},
		{"restricted image post", "https://instagram7.test/p/DaImage/", "Discordbot/2.0", scraper.ErrRestricted},
		{"browser", "https://instagram7.test/reels/DaRestricted/", "Mozilla/5.0", scraper.ErrRestricted},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.target, nil)
			request.Header.Set("User-Agent", test.userAgent)
			recorder := httptest.NewRecorder()
			if TryOGInstagramClientRedirect(recorder, request, "DaPublication", test.scrapeErr) {
				t.Fatal("unexpected bots_restricted redirect")
			}
		})
	}
}

func TestClientRedirectPreviewFallbackHandlesResolverFailuresForKnownPreviewClients(t *testing.T) {
	t.Setenv(ogInstagramClientRedirectModeEnv, ogInstagramClientRedirectPreviewFallback)
	t.Setenv("OGINSTAGRAM_PROXY_UPSTREAM", "https://og.example")

	tests := []struct {
		name      string
		target    string
		userAgent string
		scrapeErr error
		want      bool
		location  string
	}{
		{
			name:      "Telegram missing Reel",
			target:    "https://instagram7.test/reels/DaMissing/",
			userAgent: "TelegramBot (like TwitterBot)",
			scrapeErr: scraper.ErrNotFound,
			want:      true,
			location:  "https://og.example/reels/DaMissing/",
		},
		{
			name:      "Discord restricted Reel",
			target:    "https://instagram7.test/reel/DaRestricted/",
			userAgent: "Mozilla/5.0 (compatible; Discordbot/2.0)",
			scrapeErr: scraper.ErrRestricted,
			want:      true,
			location:  "https://og.example/reels/DaRestricted/",
		},
		{
			name:      "WhatsApp missing image post",
			target:    "https://instagram7.test/p/DaImage/",
			userAgent: "WhatsApp/2.0",
			scrapeErr: scraper.ErrNotFound,
			want:      true,
			location:  "https://og.example/p/DaImage/",
		},
		{
			name:      "successful preview stays local",
			target:    "https://instagram7.test/reels/DaVideo/",
			userAgent: "TelegramBot (like TwitterBot)",
			want:      false,
		},
		{
			name:      "generic scanner stays local",
			target:    "https://instagram7.test/reels/DaMissing/",
			userAgent: "Go-http-client/2.0",
			scrapeErr: scraper.ErrNotFound,
			want:      false,
		},
		{
			name:      "browser stays local",
			target:    "https://instagram7.test/reels/DaMissing/",
			userAgent: "Mozilla/5.0",
			scrapeErr: scraper.ErrNotFound,
			want:      false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.target, nil)
			request.Header.Set("User-Agent", test.userAgent)
			recorder := httptest.NewRecorder()
			got := TryOGInstagramClientRedirect(recorder, request, pathPostID(test.target), test.scrapeErr)
			if got != test.want {
				t.Fatalf("redirect = %v, want %v", got, test.want)
			}
			if test.want && recorder.Header().Get("Location") != test.location {
				t.Fatalf("Location = %q, want %q", recorder.Header().Get("Location"), test.location)
			}
		})
	}
}

func pathPostID(target string) string {
	request := httptest.NewRequest(http.MethodGet, target, nil)
	parts := []string{}
	for _, part := range strings.Split(request.URL.Path, "/") {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return parts[len(parts)-1]
}

func TestClientRedirectLegacyRestrictedMode(t *testing.T) {
	t.Setenv(ogInstagramClientRedirectModeEnv, "")
	t.Setenv(legacyOGInstagramRestrictedClientRedirectEnv, "telegram")
	t.Setenv("OGINSTAGRAM_PROXY_UPSTREAM", "https://og.example")

	request := httptest.NewRequest(http.MethodGet, "https://instagram7.test/reel/DaLegacy/", nil)
	request.Header.Set("User-Agent", "TelegramBot (like TwitterBot)")
	recorder := httptest.NewRecorder()
	if !TryOGInstagramClientRedirect(recorder, request, "DaLegacy", scraper.ErrRestricted) {
		t.Fatal("expected legacy restricted redirect")
	}
}
