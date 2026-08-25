package observability

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func managerWithTelegramServer(server *httptest.Server) *Manager {
	manager := New(Config{TelegramToken: "test-token", TelegramChat: "test-chat"})
	manager.telegram.apiBase = server.URL
	manager.telegram.client = server.Client()
	manager.telegram.maxAttempts = 2
	manager.telegram.retryDelay = 0
	manager.telegram.maxDelay = 0
	return manager
}

func TestFailureCategoryNamesInstagramContentRestrictions(t *testing.T) {
	cases := map[string]string{
		"Instagram content restricted: This content isn't available to everyone (geoblock_required)": "restricted/geoblock",
		"Instagram content restricted: People under 18 can't see this content (MIN_AGE_ACCOUNT)":     "restricted/age",
	}
	for reason, want := range cases {
		if got := failureCategory(reason); got != want {
			t.Fatalf("failureCategory(%q) = %q, want %q", reason, got, want)
		}
	}
}

func TestContentRestrictionsDoNotCountAsOperationalDegradation(t *testing.T) {
	manager := New(Config{})
	manager.RecordScrape(true, "GoodPost001", nil)
	manager.RecordScrape(false, "GeoPost001", errors.New("Instagram content restricted (geoblock_required)"))
	manager.RecordScrape(false, "AgePost001", errors.New("Instagram content restricted (MIN_AGE_ACCOUNT)"))
	manager.RecordScrape(false, "BrokenPost1", errors.New("upstream response malformed"))

	minute := time.Now().UTC().Unix() / 60
	manager.mu.Lock()
	success, failure, restricted := manager.windowLocked(minute, 15)
	details := manager.failureDetailsLocked(minute, 15)
	manager.mu.Unlock()

	if success != 1 || failure != 1 || restricted != 2 {
		t.Fatalf("window = success %d, failure %d, restricted %d; want 1, 1, 2", success, failure, restricted)
	}
	if strings.Contains(details, "GeoPost001") || strings.Contains(details, "AgePost001") {
		t.Fatalf("expected restrictions leaked into operational failure details: %q", details)
	}
	if !strings.Contains(details, "BrokenPost1") {
		t.Fatalf("operational failure missing from details: %q", details)
	}
	if got := manager.scrapeFailure.Load(); got != 3 {
		t.Fatalf("daily failure counter = %d, want all 3 final failures", got)
	}
}

func TestSendDailyReportRetainsCountersUntilConfirmed(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusServiceUnavailable)
	var reportsMu sync.Mutex
	var reports []string
	var parseModes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		reportsMu.Lock()
		reports = append(reports, r.Form.Get("text"))
		parseModes = append(parseModes, r.Form.Get("parse_mode"))
		reportsMu.Unlock()
		w.WriteHeader(int(status.Load()))
	}))
	defer server.Close()

	manager := managerWithTelegramServer(server)
	request := httptest.NewRequest(http.MethodGet, "/reels/Daily123/", nil)
	request.Header.Set("User-Agent", "TelegramBot")
	manager.RecordPreview(request, "Daily123", "full", "video")

	if err := manager.SendDailyReport(context.Background()); err == nil {
		t.Fatal("expected failed report delivery")
	}
	if got := manager.previewRequests.Load(); got != 1 {
		t.Fatalf("failed delivery reset preview counter: got %d", got)
	}
	if got := manager.previewVideos.Load(); got != 1 {
		t.Fatalf("failed delivery reset video counter: got %d", got)
	}

	status.Store(http.StatusOK)
	if err := manager.SendDailyReport(context.Background()); err != nil {
		t.Fatalf("successful report delivery returned error: %v", err)
	}
	if got := manager.previewRequests.Load(); got != 0 {
		t.Fatalf("confirmed delivery did not reset preview counter: got %d", got)
	}
	if got := manager.previewVideos.Load(); got != 0 {
		t.Fatalf("confirmed delivery did not reset video counter: got %d", got)
	}

	reportsMu.Lock()
	lastReport := reports[len(reports)-1]
	lastParseMode := parseModes[len(parseModes)-1]
	reportsMu.Unlock()
	if !strings.Contains(lastReport, "Rendered <code>1</code>") {
		t.Fatalf("confirmed report did not retain accumulated metrics: %q", lastReport)
	}
	if lastParseMode != "HTML" {
		t.Fatalf("daily report parse_mode = %q, want HTML", lastParseMode)
	}
	if !strings.Contains(lastReport, "<blockquote expandable>") {
		t.Fatalf("daily report did not contain expandable details: %q", lastReport)
	}
}

func TestSendReportPreviewDoesNotResetCounters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	manager := managerWithTelegramServer(server)
	request := httptest.NewRequest(http.MethodGet, "/reels/Preview123/", nil)
	manager.RecordPreview(request, "Preview123", "full", "video")

	if err := manager.SendReportPreview(context.Background()); err != nil {
		t.Fatalf("report preview delivery returned error: %v", err)
	}
	if got := manager.previewRequests.Load(); got != 1 {
		t.Fatalf("report preview reset counters: got %d", got)
	}
}

func TestSendDailyReportHasNoZeroActivityGate(t *testing.T) {
	var requests atomic.Int32
	var report string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		report = r.Form.Get("text")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	manager := managerWithTelegramServer(server)
	if err := manager.SendDailyReport(context.Background()); err != nil {
		t.Fatalf("zero-activity report failed: %v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("expected one Telegram request, got %d", got)
	}
	if !strings.Contains(report, "Rendered <code>0</code>") {
		t.Fatalf("zero-activity report was not sent: %q", report)
	}
}

func TestDailySketchesDoNotRotateAtMidnight(t *testing.T) {
	manager := New(Config{})
	manager.day = "2000-01-01"
	request := httptest.NewRequest(http.MethodGet, "/reels/Daily123/", nil)
	request.Header.Set("User-Agent", "TelegramBot")

	manager.RecordPreview(request, "Daily123", "full", "video")
	report := manager.DailyReport()

	if !strings.Contains(report, "2000-01-01 UTC") {
		t.Fatalf("daily boundary rotated without confirmed delivery: %q", report)
	}
	if !strings.Contains(report, "Unique <code>~1</code>") {
		t.Fatalf("preview sketch was cleared outside reset boundary: %q", report)
	}
}

func TestDailyReportIncludesMediaStreamAndOGFallbackMetrics(t *testing.T) {
	manager := New(Config{})
	request := httptest.NewRequest(http.MethodGet, "/reels/Daily123/", nil)
	request.Header.Set("User-Agent", "TelegramBot")

	manager.RecordMediaStream(http.MethodHead, false, http.StatusOK, 0, nil)
	manager.RecordMediaStream(http.MethodGet, true, http.StatusPartialContent, 1024, nil)
	manager.RecordMediaStream(http.MethodGet, false, http.StatusBadGateway, 0, errors.New("upstream failed"))
	manager.RecordOGProxyFallback(request, "Daily123", "read failed")
	manager.RecordOGProxyFallback(request, "Daily123", "read failed")
	manager.RecordOGProxyUpstreamFailure(request, "Daily123")

	report := manager.DailyReport()
	for _, want := range []string{
		"HEAD <code>1</code> · GET <code>2</code> · Range <code>1</code>",
		"HTTP 200 <code>1</code> · 206 <code>1</code> · Failed <code>1</code>",
		"Traffic <code>1.0 KB</code>",
		"Server proxy served <code>0</code>",
		"Proxy fallback to local <code>3</code>",
		"Upstream failures <code>1</code>",
		"read_failed: 2",
		"upstream_failure: 1",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("expected report to contain %q, got %q", want, report)
		}
	}
}

func TestDailyReportBreaksDownPreviewFailureCauses(t *testing.T) {
	manager := New(Config{})
	request := httptest.NewRequest(http.MethodGet, "/reels/DbK4jO6Nh0K/", nil)
	request.Header.Set("User-Agent", "TelegramBot")

	headers := make(http.Header)
	headers.Set("Server", "cloudflare")
	headers.Set("Cf-Mitigated", "challenge")
	manager.RecordOGProxyHTTPFailure(request, "DbK4jO6Nh0K", http.StatusForbidden, headers)
	manager.RecordOGProxyFallback(request, "DbK4jO6Nh0K", "read failed")
	manager.RecordOGProxyUpstreamFailure(request, "DbK4jO6Nh0K")
	manager.RecordScrape(false, "DbK4jO6Nh0K", errors.New("public refresh returned no video"))
	manager.RecordScrape(false, "DbK4jO6Nh0K", errors.New("post not found; authenticated fallback unavailable: cookie pool unavailable"))
	manager.RecordAuthHelperResult(false, "DbK4jO6Nh0K", "login_required", errors.New("redirected to /accounts/login/"))
	manager.RecordAuthHelperResult(false, "DbK4jO6Nh0K", "auth_helper_unreachable", errors.New("connection refused"))

	report := manager.DailyReport()
	for _, want := range []string{
		"CF 403 <code>1</code> · Other <code>2</code>",
		"No video <code>1</code> · Not found <code>1</code>",
		"Unavailable <code>1</code> · Login redirects <code>1</code>",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("expected report to contain %q, got %q", want, report)
		}
	}
	if strings.Contains(report, "connection refused") || strings.Contains(report, "cookie pool unavailable") {
		t.Fatalf("report leaked raw failure detail: %q", report)
	}
}

func TestDailyReportFailureCauseCountersResetAfterConfirmedDelivery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	manager := managerWithTelegramServer(server)
	request := httptest.NewRequest(http.MethodGet, "/reels/DbK4jO6Nh0K/", nil)
	headers := make(http.Header)
	headers.Set("Cf-Mitigated", "challenge")
	manager.RecordOGProxyHTTPFailure(request, "DbK4jO6Nh0K", http.StatusForbidden, headers)
	manager.RecordScrape(false, "DbK4jO6Nh0K", errors.New("post not found"))
	manager.RecordAuthHelperResult(false, "DbK4jO6Nh0K", "login_required", nil)

	if err := manager.SendDailyReport(context.Background()); err != nil {
		t.Fatalf("confirmed delivery returned error: %v", err)
	}

	report := manager.DailyReport()
	for _, unwanted := range []string{"<b>Auth helper</b>", "<b>OGInstagram</b>"} {
		if strings.Contains(report, unwanted) {
			t.Fatalf("reset report retained empty section %q: %q", unwanted, report)
		}
	}
	if got := manager.localResolverNotFound.Load(); got != 0 {
		t.Fatalf("local resolver reason was not reset: %d", got)
	}
}

func TestAuthSessionFailuresRequestCookieRecovery(t *testing.T) {
	requestFile := t.TempDir() + "/cookie-recovery.json"
	t.Setenv("COOKIE_RECOVERY_REQUEST_FILE", requestFile)
	manager := New(Config{})

	for range 3 {
		manager.RecordAuthHelperResult(false, "DaExpired", "login_required", errors.New("redirected to login"))
	}

	body, err := os.ReadFile(requestFile)
	if err != nil {
		t.Fatalf("recovery request was not written: %v", err)
	}
	for _, want := range []string{`"post_id":"DaExpired"`, `"code":"login_required"`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("recovery request missing %q: %s", want, body)
		}
	}
}

func TestDailyReportSummarizesTrafficPeaksAndClientTypes(t *testing.T) {
	manager := New(Config{})
	manager.RecordMetadataProxyResult(true, 2048, 256*1024)
	manager.RecordMetadataProxyResult(false, 1024, 0)
	manager.RecordMediaStream(http.MethodGet, false, http.StatusOK, 25_259_668_384, nil)
	finishFirst := manager.BeginMediaStream()
	finishSecond := manager.BeginMediaStream()
	finishSecond()
	finishFirst()
	manager.mu.Lock()
	manager.peakRequestsPerMinute = 321
	manager.mu.Unlock()

	for userAgent, count := range map[string]int{
		"TelegramBot":      1,
		"Discordbot":       2,
		"WhatsApp/2.0":     3,
		"Mozilla/5.0":      4,
		"ExampleCrawler/1": 5,
	} {
		for i := 0; i < count; i++ {
			request := httptest.NewRequest(http.MethodGet, "/reel/Client1/", nil)
			request.Header.Set("User-Agent", userAgent)
			manager.RecordPreview(request, "Client1", "full", "video")
		}
	}

	report := manager.DailyReport()
	for _, want := range []string{
		"Video served: <b>25.3 GB</b>",
		"321 HTTP requests/min · 2 concurrent streams",
		"Telegram <code>1</code> · Discord <code>2</code>",
		"WhatsApp <code>3</code> · Browsers <code>4</code> · Other bots <code>5</code>",
		"Requests <code>2</code> · Success <code>1</code> · Failed <code>1</code>",
		"Payload up <code>3.1 KB</code> · down <code>262.1 KB</code>",
		"🎯 <b>Previews</b>",
		"📡 <b>Media streams</b>",
		"⚙️ <b>Runtime</b>",
		"<blockquote expandable>",
		"</blockquote>",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("expected report to contain %q, got %q", want, report)
		}
	}
	if got := len([]rune(report)); got > 4096 {
		t.Fatalf("daily report is %d characters, exceeds Telegram limit", got)
	}
}

func TestDailyReportMessengerOutcomesExcludeOtherClients(t *testing.T) {
	manager := New(Config{})
	for _, tc := range []struct {
		ua, outcome, media string
	}{
		{"TelegramBot", "full", "video"},
		{"TelegramBot", "fallback", "generic"},
		{"Discordbot", "failed", ""},
		{"WhatsApp/2.0", "full", "image"},
		{"Go-http-client/2.0", "fallback", "generic"},
		{"Mozilla/5.0", "full", "video"},
	} {
		request := httptest.NewRequest(http.MethodGet, "/reel/Messenger1/", nil)
		request.Header.Set("User-Agent", tc.ua)
		manager.RecordPreview(request, "Messenger1", tc.outcome, tc.media)
	}

	if got := manager.previewTelegramFull.Load(); got != 1 {
		t.Fatalf("telegram full = %d, want 1", got)
	}
	if got := manager.previewTelegramFallback.Load(); got != 1 {
		t.Fatalf("telegram fallback = %d, want 1", got)
	}
	if got := manager.previewDiscordFailed.Load(); got != 1 {
		t.Fatalf("discord failed = %d, want 1", got)
	}
	if got := manager.previewWhatsAppFull.Load(); got != 1 {
		t.Fatalf("whatsapp full = %d, want 1", got)
	}
	if got := manager.previewMessengerVideos.Load(); got != 1 {
		t.Fatalf("messenger videos = %d, want 1", got)
	}
	if got := manager.previewMessengerImages.Load(); got != 1 {
		t.Fatalf("messenger images = %d, want 1", got)
	}

	report := manager.DailyReport()
	for _, want := range []string{
		"Messenger full previews: 2/4</b> (50.0%)",
		"Messenger fallbacks: <b>1</b> · Failed: 1",
		"Messenger videos: <b>1</b> · 🖼 Images: 1",
		"Other crawler/browser requests: 2 · Fallback: 1 · Failed: 0",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("expected report to contain %q, got %q", want, report)
		}
	}
}

func TestPreviewRateAndBoundedSanitizedDetails(t *testing.T) {
	manager := New(Config{})
	request := httptest.NewRequest(http.MethodGet, "/reels/Detail123/", nil)
	request.Header.Set("User-Agent", "TelegramBot")

	manager.RecordPreviewWithReason(request, "Detail123", "full", "video", "")
	manager.RecordPreviewWithReason(request, "Detail456", "fallback", "generic", "resolver failed")
	manager.RecordPreviewWithReason(request, "Detail789", "failed", "", "token=must-not-leak")
	manager.RecordPreviewWithReason(request, "https://instagram.com/reel/leak", "fallback", "generic", "secret")
	for i := 0; i < maxDailyDetailEntries+20; i++ {
		manager.RecordPreviewWithReason(request, fmt.Sprintf("D%06d", i), "fallback", "generic", fmt.Sprintf("reason %d", i))
	}
	for range 5 {
		manager.RecordPreviewWithReason(request, "Detail456", "fallback", "generic", "resolver failed")
	}

	if got := len(manager.previewFallbackDetails); got != maxDailyDetailEntries {
		t.Fatalf("fallback detail map size = %d, want bounded %d", got, maxDailyDetailEntries)
	}
	for key := range manager.previewFallbackDetails {
		if !validPostID(key.postID) || strings.Contains(key.reason, "://") {
			t.Fatalf("unsafe fallback detail stored: %#v", key)
		}
	}
	for key := range manager.previewFailedDetails {
		if key.reason == "token_must_not_leak" || strings.Contains(key.reason, "must_not_leak") {
			t.Fatalf("sensitive raw reason stored: %#v", key)
		}
	}

	report := manager.DailyReport()
	for _, want := range []string{
		"Messenger full previews: 1/157</b> (0.6%)",
		"Messenger fallbacks: <b>155</b> · Failed: 1",
		"<code>Detail456</code> resolver_failed/telegram ×6",
		"<code>Detail789</code> other/telegram",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("expected report to contain %q, got %q", want, report)
		}
	}
	for _, leaked := range []string{"must-not-leak", "token_must_not_leak", "https://instagram.com"} {
		if strings.Contains(report, leaked) {
			t.Fatalf("report leaked %q: %q", leaked, report)
		}
	}
}

func TestDailyReportIncludesTelegramReelDecisionDetails(t *testing.T) {
	manager := New(Config{})
	manager.RecordTelegramVideoDecision("DbQM5BJsFGI", "compact", "oversized_over_20_mib", 31_434_927)
	manager.RecordTelegramVideoDelivery("DbQM5BJsFGI", "smart", 19_828_247, "")
	manager.RecordTelegramVideoDecision("DcUVzuCPgdE", "direct", "within_20_mib", 15_416_399)
	manager.RecordTelegramVideoDecision("Blocked123", "blocked", "age_restricted_21", 0)
	manager.RecordTelegramVideoDecision("Image123", "expected_image", "oversized_compact_unavailable", 25_000_000)

	report := manager.DailyReport()
	for _, want := range []string{
		"<b>Telegram Reels:</b> direct 1 · compact 1 · expected image 1 · blocked 1",
		"DbQM5BJsFGI</a> — 🗜 compact · original 31.4 MB · >20 MiB → smart 19.8 MB",
		"DcUVzuCPgdE</a> — ✅ direct · original 15.4 MB · ≤20 MiB",
		"Blocked123</a> — 🚫 blocked · 21+ restriction",
		"Image123</a> — 🖼 expected image · original 25.0 MB · >20 MiB · compact unavailable",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("expected report to contain %q, got %q", want, report)
		}
	}

	manager.resetDaily()
	if report := manager.DailyReport(); strings.Contains(report, "<b>Telegram Reels:</b>") {
		t.Fatalf("Telegram Reel details survived daily reset: %q", report)
	}
}

func TestDailyReportOmitsInactiveOptionalSections(t *testing.T) {
	manager := New(Config{})
	report := manager.DailyReport()
	for _, unwanted := range []string{
		"<b>Residential metadata proxy</b>",
		"<b>Auth helper</b>",
		"<b>OGInstagram</b>",
		"Cookies/auth-helper: not used",
		"OGInstagram: not used",
	} {
		if strings.Contains(report, unwanted) {
			t.Fatalf("inactive report contained %q: %q", unwanted, report)
		}
	}
}

func TestAuthMetricsSplitCacheAndAvoidDoubleCounting(t *testing.T) {
	manager := New(Config{})

	manager.RecordAuthHelperResult(true, "Auth001", "ok", nil)
	manager.RecordAuthHelperResult(true, "Auth002", "cache_hit", nil)
	manager.RecordAuthHelperResult(false, "Auth003", "invalid_response", errors.New("bad response"))
	manager.RecordAuthHelperResult(false, "Auth004", "cache_hit:login_required", errors.New("cached login failure"))
	manager.RecordAuthHelperSkipped("Auth005", "pool empty")
	manager.RecordScrape(false, "Auth006", errors.New("authenticated fallback unavailable: cookie pool unavailable"))

	if got := manager.authUsed.Load(); got != 2 {
		t.Fatalf("auth success = %d, want 2", got)
	}
	if got := manager.authCacheHits.Load(); got != 1 {
		t.Fatalf("auth success cache = %d, want 1", got)
	}
	if got := manager.authFailed.Load(); got != 2 {
		t.Fatalf("auth failed = %d, want exactly 2", got)
	}
	if got := manager.authCachedFailures.Load(); got != 1 {
		t.Fatalf("auth cached failures = %d, want 1", got)
	}
	if got := manager.authSkipped.Load(); got != 1 {
		t.Fatalf("auth skipped = %d, want 1", got)
	}
	if got := manager.authSessionFailures.Load(); got != 1 {
		t.Fatalf("auth session failures = %d, want cached underlying login failure to count", got)
	}
	if got := manager.authFallbackUnavailable.Load(); got != 0 {
		t.Fatalf("RecordScrape duplicated auth unavailable count: %d", got)
	}

	report := manager.DailyReport()
	for _, want := range []string{
		"Success <code>2</code>: upstream <code>1</code> · cache <code>1</code>",
		"Failed <code>2</code>: upstream <code>1</code> · cache <code>1</code>",
		"Skipped without auth request <code>1</code> · Session failures <code>1</code>",
		"Auth helper: 1 upstream success · 1 upstream failed · 2 cached results\n   1 skipped without auth request",
		"<code>Auth004</code> cache_failed_login_required",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("expected auth report to contain %q, got %q", want, report)
		}
	}
}

func TestAuthHelperFailureCountedOnce(t *testing.T) {
	manager := New(Config{})
	manager.RecordAuthHelperResult(false, "Auth007", "auth_helper_unreachable", errors.New("connection refused"))

	if got := manager.authFailed.Load(); got != 1 {
		t.Fatalf("auth failure counted %d times, want 1", got)
	}
	if got := manager.authFallbackUnavailable.Load(); got != 1 {
		t.Fatalf("auth unavailable = %d, want 1", got)
	}
	if got := manager.authSessionFailures.Load(); got != 0 {
		t.Fatalf("transport failure counted as session failure: %d", got)
	}
}

func TestAuthOEmbedUnavailableIsNotHelperUnavailable(t *testing.T) {
	manager := New(Config{})
	manager.RecordAuthHelperResult(false, "Auth008", "auth_oembed_unavailable", errors.New("Instagram oEmbed rejected this media"))

	if got := manager.authFailed.Load(); got != 1 {
		t.Fatalf("auth failure counted %d times, want 1", got)
	}
	if got := manager.authFallbackUnavailable.Load(); got != 0 {
		t.Fatalf("Instagram media rejection counted as helper unavailable: %d", got)
	}
}

func TestOGClientRedirectMetricsAndDetails(t *testing.T) {
	manager := New(Config{})
	request := httptest.NewRequest(http.MethodGet, "/reels/Redirect1/", nil)
	request.Header.Set("User-Agent", "Slackbot-LinkExpanding 1.0")

	manager.RecordOGClientRedirect(request, "Redirect1", "restricted redirect")
	manager.RecordOGClientRedirect(request, "Redirect1", "restricted redirect")
	manager.RecordOGClientRedirect(request, "https://invalid.example/secret", "token=secret")

	if got := manager.ogClientRedirects.Load(); got != 3 {
		t.Fatalf("OG redirects = %d, want 3", got)
	}
	if got := len(manager.ogClientRedirectDetails); got != 1 {
		t.Fatalf("OG redirect details = %d, want only one safe key", got)
	}
	report := manager.DailyReport()
	for _, want := range []string{
		"Cross-domain redirects <code>3</code>",
		"<code>Redirect1</code> restricted_redirect/bot ×2",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("expected OG report to contain %q, got %q", want, report)
		}
	}
	if strings.Contains(report, "invalid.example") || strings.Contains(report, "token_secret") {
		t.Fatalf("OG report leaked unsafe detail: %q", report)
	}
}

func TestConfirmedReportResetsNewDailyState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	manager := managerWithTelegramServer(server)
	request := httptest.NewRequest(http.MethodGet, "/reels/Reset123/", nil)
	manager.RecordPreviewWithReason(request, "Reset123", "fallback", "generic", "resolver_failed")
	manager.RecordAuthHelperSkipped("Reset123", "pool_empty")
	manager.RecordOGClientRedirect(request, "Reset123", "restricted")

	if err := manager.SendDailyReport(context.Background()); err != nil {
		t.Fatalf("confirmed report failed: %v", err)
	}
	if manager.previewFallback.Load() != 0 || manager.authSkipped.Load() != 0 || manager.ogClientRedirects.Load() != 0 ||
		manager.previewTelegramFallback.Load() != 0 || manager.previewMessengerVideos.Load() != 0 {
		t.Fatal("new counters were not reset")
	}
	if len(manager.previewFallbackDetails) != 0 || len(manager.authDetails) != 0 || len(manager.ogClientRedirectDetails) != 0 {
		t.Fatal("new detail maps were not reset")
	}
}

func TestDailyReportStaysWithinTelegramLimit(t *testing.T) {
	manager := New(Config{})
	request := httptest.NewRequest(http.MethodGet, "/reels/Length00/", nil)
	request.Header.Set("User-Agent", "TelegramBot")

	for i := 0; i < maxDailyDetailEntries*2; i++ {
		postID := fmt.Sprintf("D%06d", i)
		manager.RecordPreviewWithReason(request, postID, "fallback", "generic", fmt.Sprintf("fallback_reason_%d", i))
		manager.RecordPreviewWithReason(request, postID, "failed", "", fmt.Sprintf("failed_reason_%d", i))
		manager.RecordAuthHelperSkipped(postID, fmt.Sprintf("skip_reason_%d", i))
		manager.RecordOGClientRedirect(request, postID, fmt.Sprintf("redirect_reason_%d", i))
	}
	manager.RecordMetadataProxyResult(false, 123456, 654321)

	report := manager.DailyReport()
	if got := len([]rune(report)); got > 4096 {
		t.Fatalf("daily report is %d characters, exceeds Telegram limit", got)
	}
	if !strings.HasSuffix(report, "</blockquote>") {
		t.Fatalf("daily report has invalid truncated HTML ending: %q", report[len(report)-64:])
	}
}

func TestClientClassUsesGenericBotDetection(t *testing.T) {
	for _, ua := range []string{
		"curl/8.7.1",
		"python-requests/2.32",
		"Link Preview Service/1.0",
		"Slackbot-LinkExpanding 1.0",
	} {
		if got := clientClass(ua); got != "bot" {
			t.Fatalf("clientClass(%q) = %q, want bot", ua, got)
		}
	}
	if got := clientClass("Mozilla/5.0 Chrome/126.0"); got != "browser" {
		t.Fatalf("browser UA classified as %q", got)
	}
}
