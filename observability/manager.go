package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"instafix/utils"
)

type Config struct {
	TelegramToken string
	TelegramChat  string
	ReportHourUTC int
}

type scrapeBucket struct {
	minute                       int64
	success, failure, restricted uint64
	failures                     []scrapeFailure
}

type scrapeFailure struct {
	postID string
	reason string
}

type dailyDetailKey struct {
	postID string
	reason string
	client string
}

type dailyTelegramVideo struct {
	Requests       uint64
	Decision       string
	Reason         string
	OriginalBytes  int64
	Delivery       string
	DeliveredBytes int64
	DeliveryReason string
}

const (
	maxDailyDetailEntries = 128
	maxDailyReportRunes   = 3900
)

type Manager struct {
	started                                                      time.Time
	telegram                                                     *TelegramClient
	requests, cacheHits, scrapeSuccess, scrapeFailure, dbErrors  atomic.Uint64
	previewRequests, previewFull, previewFallback, previewFailed atomic.Uint64
	previewVideos, previewImages, previewGeneric                 atomic.Uint64
	previewTelegram, previewDiscord, previewWhatsApp             atomic.Uint64
	previewBrowsers, previewBots                                 atomic.Uint64
	ogProxyServed, ogProxyFallback, ogProxyUpstreamFailures      atomic.Uint64
	ogProxyCloudflare403, ogProxyOtherFailures                   atomic.Uint64
	ogClientRedirects                                            atomic.Uint64
	mediaStreamHead, mediaStreamGet, mediaStreamRange            atomic.Uint64
	mediaStream200, mediaStream206, mediaStreamFailures          atomic.Uint64
	mediaStreamBytes                                             atomic.Uint64
	largestMediaStreamBytes, peakRuntimeBytes, peakGoroutines    atomic.Uint64
	activeMediaStreams, peakMediaStreams                         atomic.Int64
	authUsed, authCacheHits, authFailed, authCachedFailures      atomic.Uint64
	authSkipped                                                  atomic.Uint64
	authSessionFailures, authCookiePoolFailures                  atomic.Uint64
	authFallbackUnavailable, authLoginRedirect                   atomic.Uint64
	metadataProxyRequests, metadataProxySuccess                  atomic.Uint64
	metadataProxyFailures, metadataProxyUploadBytes              atomic.Uint64
	metadataProxyDownloadBytes                                   atomic.Uint64
	localResolverNoVideo, localResolverNotFound                  atomic.Uint64
	status                                                       [6]atomic.Uint64
	mu                                                           sync.Mutex
	day                                                          string
	users, posts, previewPosts                                   []uint64
	previewFallbackPosts, previewFailedPosts                     []uint64
	failureReasons                                               map[string]uint64
	ogProxyFallbackReasons                                       map[string]uint64
	previewFallbackDetails, previewFailedDetails                 map[dailyDetailKey]uint64
	authDetails, ogClientRedirectDetails                         map[dailyDetailKey]uint64
	telegramVideoDetails                                         map[string]dailyTelegramVideo
	buckets                                                      [15]scrapeBucket
	alertActive                                                  bool
	lastAlert                                                    time.Time
	lastDBAlert                                                  time.Time
	authFailureStreak                                            int
	authAlertActive                                              bool
	lastAuthAlert                                                time.Time
	reportHour                                                   int
	requestMinute                                                int64
	requestsThisMinute, peakRequestsPerMinute                    uint64

	previewTelegramFull, previewTelegramFallback, previewTelegramFailed atomic.Uint64
	previewDiscordFull, previewDiscordFallback, previewDiscordFailed    atomic.Uint64
	previewWhatsAppFull, previewWhatsAppFallback, previewWhatsAppFailed atomic.Uint64
	previewMessengerVideos, previewMessengerImages                      atomic.Uint64
	telegramVideoDirect, telegramVideoCompact                           atomic.Uint64
	telegramVideoExpectedImage, telegramVideoBlocked                    atomic.Uint64
}

var Default = New(Config{})
var dailyReportRetryDelay = 15 * time.Minute

func New(cfg Config) *Manager {
	hour := cfg.ReportHourUTC
	if hour < 0 || hour > 23 {
		hour = 0
	}
	m := &Manager{
		started:                 time.Now().UTC(),
		telegram:                NewTelegramClient(cfg.TelegramToken, cfg.TelegramChat),
		users:                   make([]uint64, 1024),
		posts:                   make([]uint64, 1024),
		previewPosts:            make([]uint64, 1024),
		previewFallbackPosts:    make([]uint64, 256),
		previewFailedPosts:      make([]uint64, 256),
		failureReasons:          make(map[string]uint64),
		ogProxyFallbackReasons:  make(map[string]uint64),
		previewFallbackDetails:  make(map[dailyDetailKey]uint64),
		previewFailedDetails:    make(map[dailyDetailKey]uint64),
		authDetails:             make(map[dailyDetailKey]uint64),
		ogClientRedirectDetails: make(map[dailyDetailKey]uint64),
		telegramVideoDetails:    make(map[string]dailyTelegramVideo),
		reportHour:              hour,
	}
	m.day = m.started.Format("2006-01-02")
	m.requestMinute = m.started.Unix() / 60
	m.sampleRuntime()
	return m
}

func Configure(cfg Config) { Default = New(cfg) }

func hash(s string) uint64 { h := fnv.New64a(); _, _ = h.Write([]byte(s)); return h.Sum64() }

func addEstimate(bits []uint64, value string) {
	n := hash(value) % uint64(len(bits)*64)
	bits[n/64] |= 1 << (n % 64)
}
func estimate(bits []uint64) int {
	zero := 0
	for _, b := range bits {
		zero += 64 - bitsOnesCount(b)
	}
	if zero == 0 {
		return len(bits) * 64
	}
	m := float64(len(bits) * 64)
	return int(-m * math.Log(float64(zero)/m))
}
func bitsOnesCount(x uint64) int {
	n := 0
	for x != 0 {
		x &= x - 1
		n++
	}
	return n
}

func (m *Manager) RecordCacheHit() { m.cacheHits.Add(1) }

func (m *Manager) RecordPreview(r *http.Request, postID, outcome, media string) {
	m.RecordPreviewWithReason(r, postID, outcome, media, "")
}

func (m *Manager) RecordPreviewWithReason(r *http.Request, postID, outcome, media, reason string) {
	m.previewRequests.Add(1)
	client := requestClientClass(r)
	switch outcome {
	case "full":
		m.previewFull.Add(1)
	case "fallback":
		m.previewFallback.Add(1)
		m.mu.Lock()
		if validPostID(postID) {
			addEstimate(m.previewFallbackPosts, postID)
		}
		addDailyDetail(m.previewFallbackDetails, postID, reason, client)
		m.mu.Unlock()
	default:
		m.previewFailed.Add(1)
		m.mu.Lock()
		if validPostID(postID) {
			addEstimate(m.previewFailedPosts, postID)
		}
		addDailyDetail(m.previewFailedDetails, postID, reason, client)
		m.mu.Unlock()
	}
	m.recordMessengerOutcome(client, outcome)
	switch media {
	case "video":
		m.previewVideos.Add(1)
		if isMessengerClient(client) {
			m.previewMessengerVideos.Add(1)
		}
	case "generic":
		m.previewGeneric.Add(1)
	case "image":
		m.previewImages.Add(1)
		if isMessengerClient(client) {
			m.previewMessengerImages.Add(1)
		}
	}
	switch client {
	case "telegram":
		m.previewTelegram.Add(1)
	case "discord":
		m.previewDiscord.Add(1)
	case "whatsapp":
		m.previewWhatsApp.Add(1)
	case "bot":
		m.previewBots.Add(1)
	default:
		m.previewBrowsers.Add(1)
	}
	m.mu.Lock()
	if postID != "" {
		addEstimate(m.previewPosts, postID)
	}
	m.mu.Unlock()
}

func (m *Manager) RecordTelegramVideoDecision(postID, decision, reason string, originalBytes int64) {
	if m == nil || !validPostID(postID) {
		return
	}
	decision = normalizeTelegramVideoDecision(decision)
	switch decision {
	case "direct":
		m.telegramVideoDirect.Add(1)
	case "compact":
		m.telegramVideoCompact.Add(1)
	case "expected_image":
		m.telegramVideoExpectedImage.Add(1)
	case "blocked":
		m.telegramVideoBlocked.Add(1)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, exists := m.telegramVideoDetails[postID]
	if !exists && len(m.telegramVideoDetails) >= maxDailyDetailEntries {
		return
	}
	entry.Requests++
	entry.Decision = decision
	entry.Reason = safeDetailReason(reason)
	if originalBytes > 0 {
		entry.OriginalBytes = originalBytes
	}
	m.telegramVideoDetails[postID] = entry
}

func (m *Manager) RecordTelegramVideoDelivery(postID, delivery string, deliveredBytes int64, reason string) {
	if m == nil || !validPostID(postID) {
		return
	}
	delivery = normalizeTelegramVideoDelivery(delivery)
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, exists := m.telegramVideoDetails[postID]
	if !exists && len(m.telegramVideoDetails) >= maxDailyDetailEntries {
		return
	}
	entry.Delivery = delivery
	if deliveredBytes > 0 {
		entry.DeliveredBytes = deliveredBytes
	}
	if strings.TrimSpace(reason) != "" {
		entry.DeliveryReason = safeDetailReason(reason)
	}
	m.telegramVideoDetails[postID] = entry
}

func normalizeTelegramVideoDecision(decision string) string {
	switch strings.ToLower(strings.TrimSpace(decision)) {
	case "direct":
		return "direct"
	case "compact":
		return "compact"
	case "expected_image":
		return "expected_image"
	default:
		return "blocked"
	}
}

func normalizeTelegramVideoDelivery(delivery string) string {
	switch strings.ToLower(strings.TrimSpace(delivery)) {
	case "smart":
		return "smart"
	case "dash":
		return "dash"
	case "failed":
		return "failed"
	default:
		return "unknown"
	}
}

func isMessengerClient(client string) bool {
	return client == "telegram" || client == "discord" || client == "whatsapp"
}

func (m *Manager) RecordOGProxyServed(r *http.Request, postID, media string) {
	m.ogProxyServed.Add(1)
	m.RecordPreview(r, postID, "full", media)
}

func (m *Manager) RecordOGClientRedirect(r *http.Request, postID, reason string) {
	m.ogClientRedirects.Add(1)
	client := requestClientClass(r)
	m.mu.Lock()
	addDailyDetail(m.ogClientRedirectDetails, postID, reason, client)
	m.mu.Unlock()
	slog.Info("oginstagram client redirect counted", "postID", postID, "reason", normalizeReason(reason), "client", client)
}

func (m *Manager) RecordOGProxyFallback(r *http.Request, postID, reason string) {
	m.ogProxyFallback.Add(1)
	if isOGCloudflareChallenge(reason) {
		m.ogProxyCloudflare403.Add(1)
	} else if normalizeReason(reason) != "circuit_open" {
		m.ogProxyOtherFailures.Add(1)
	}
	m.mu.Lock()
	m.ogProxyFallbackReasons[normalizeReason(reason)]++
	m.mu.Unlock()
	slog.Info("oginstagram proxy fallback counted", "postID", postID, "reason", reason, "client", clientClass(r.UserAgent()))
}

func (m *Manager) RecordOGProxyUpstreamFailure(r *http.Request, postID string) {
	m.ogProxyFallback.Add(1)
	m.ogProxyUpstreamFailures.Add(1)
	m.ogProxyOtherFailures.Add(1)
	m.mu.Lock()
	m.ogProxyFallbackReasons["upstream_failure"]++
	m.mu.Unlock()
	slog.Info("oginstagram proxy upstream failure counted", "postID", postID, "client", clientClass(r.UserAgent()))
}

func (m *Manager) RecordOGProxyHTTPFailure(r *http.Request, postID string, status int, headers http.Header) {
	m.ogProxyFallback.Add(1)
	m.ogProxyUpstreamFailures.Add(1)
	reason := "http_" + strconv.Itoa(status)
	cloudflare := status == http.StatusForbidden &&
		(strings.EqualFold(strings.TrimSpace(headers.Get("Cf-Mitigated")), "challenge") ||
			strings.EqualFold(strings.TrimSpace(headers.Get("Server")), "cloudflare"))
	if cloudflare {
		m.ogProxyCloudflare403.Add(1)
		reason = "cloudflare_challenge_403"
	} else {
		m.ogProxyOtherFailures.Add(1)
	}
	m.mu.Lock()
	m.ogProxyFallbackReasons[reason]++
	m.mu.Unlock()
	slog.Info("oginstagram proxy HTTP failure counted", "postID", postID, "status", status, "reason", reason, "client", clientClass(r.UserAgent()))
}

func (m *Manager) RecordMetadataProxyResult(success bool, uploadBytes, downloadBytes int64) {
	m.metadataProxyRequests.Add(1)
	if success {
		m.metadataProxySuccess.Add(1)
	} else {
		m.metadataProxyFailures.Add(1)
	}
	if uploadBytes > 0 {
		m.metadataProxyUploadBytes.Add(uint64(uploadBytes))
	}
	if downloadBytes > 0 {
		m.metadataProxyDownloadBytes.Add(uint64(downloadBytes))
	}
}

func (m *Manager) recordMessengerOutcome(client, outcome string) {
	var counter *atomic.Uint64
	switch client {
	case "telegram":
		switch outcome {
		case "full":
			counter = &m.previewTelegramFull
		case "fallback":
			counter = &m.previewTelegramFallback
		default:
			counter = &m.previewTelegramFailed
		}
	case "discord":
		switch outcome {
		case "full":
			counter = &m.previewDiscordFull
		case "fallback":
			counter = &m.previewDiscordFallback
		default:
			counter = &m.previewDiscordFailed
		}
	case "whatsapp":
		switch outcome {
		case "full":
			counter = &m.previewWhatsAppFull
		case "fallback":
			counter = &m.previewWhatsAppFallback
		default:
			counter = &m.previewWhatsAppFailed
		}
	}
	if counter != nil {
		counter.Add(1)
	}
}

// RecordMediaStream records an actual media endpoint response. bytes should be
// the number of response body bytes copied, or zero for HEAD and failures.
func (m *Manager) RecordMediaStream(method string, rangeRequest bool, status int, bytes int64, err error) {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case http.MethodHead:
		m.mediaStreamHead.Add(1)
	case http.MethodGet:
		m.mediaStreamGet.Add(1)
	}
	if rangeRequest {
		m.mediaStreamRange.Add(1)
	}
	switch status {
	case http.StatusOK:
		m.mediaStream200.Add(1)
	case http.StatusPartialContent:
		m.mediaStream206.Add(1)
	}
	if err != nil || status < 200 || status >= 300 {
		m.mediaStreamFailures.Add(1)
	}
	if bytes > 0 {
		m.mediaStreamBytes.Add(uint64(bytes))
		updateAtomicMax(&m.largestMediaStreamBytes, uint64(bytes))
	}
}

func (m *Manager) BeginMediaStream() func() {
	if m == nil {
		return func() {}
	}
	active := m.activeMediaStreams.Add(1)
	for {
		peak := m.peakMediaStreams.Load()
		if active <= peak || m.peakMediaStreams.CompareAndSwap(peak, active) {
			break
		}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			m.activeMediaStreams.Add(-1)
		})
	}
}

func updateAtomicMax(counter *atomic.Uint64, value uint64) {
	for {
		current := counter.Load()
		if value <= current || counter.CompareAndSwap(current, value) {
			return
		}
	}
}

func (m *Manager) sampleRuntime() {
	if m == nil {
		return
	}
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	updateAtomicMax(&m.peakRuntimeBytes, stats.Sys)
	updateAtomicMax(&m.peakGoroutines, uint64(runtime.NumGoroutine()))
}

func (m *Manager) RecordScrape(success bool, postID string, err error) {
	if success {
		m.scrapeSuccess.Add(1)
	} else {
		m.scrapeFailure.Add(1)
	}
	now := time.Now().UTC()
	minute := now.Unix() / 60
	m.mu.Lock()
	if postID != "" {
		addEstimate(m.posts, postID)
	}
	b := &m.buckets[minute%15]
	if b.minute != minute {
		*b = scrapeBucket{minute: minute}
	}
	if success {
		b.success++
	} else {
		reason := conciseError(err)
		category := failureCategory(reason)
		if isExpectedContentRestriction(category) {
			b.restricted++
		} else {
			b.failure++
			b.failures = append(b.failures, scrapeFailure{postID: postID, reason: category})
		}
		m.failureReasons[category]++
		m.recordLocalResolverFailure(reason)
	}
	s, f, restricted := m.windowLocked(minute, 15)
	failureDetails := m.failureDetailsLocked(minute, 15)
	active := s+f >= 10 && float64(f)/float64(s+f) > .30
	outage := s+f >= 5 && s == 0
	shouldAlert := (active || outage) && (!m.alertActive || now.Sub(m.lastAlert) >= 30*time.Minute)
	recovered := m.alertActive && s > 0 && !(active || outage)
	if shouldAlert {
		m.alertActive = true
		m.lastAlert = now
	}
	if recovered {
		m.alertActive = false
	}
	m.mu.Unlock()
	if shouldAlert {
		m.Alert(fmt.Sprintf("🚨 InstaFix preview scrape degraded\n15m success: %d\n15m operational failures: %d\nExpected content restrictions: %d\nOperational failure rate: %.1f%%\nNo full scrapes succeeded: %t%s%s", s, f, restricted, 100*float64(f)/float64(s+f), outage, failureDetails, authCookieStatus()))
	}
	if recovered {
		m.Alert(fmt.Sprintf("✅ InstaFix scrape recovered\n15m success: %d\n15m operational failures: %d\nExpected content restrictions: %d%s%s", s, f, restricted, failureDetails, authCookieStatus()))
	}
	if !success {
		slog.Error("final Instagram scrape failed", "event", "scrape_final", "post_id", postID, "err", err)
	} else {
		slog.Info("Instagram scrape succeeded", "event", "scrape_final", "post_id", postID)
	}
}

func conciseError(err error) string {
	if err == nil {
		return "unknown error"
	}
	reason := strings.Join(strings.Fields(err.Error()), " ")
	const maxRunes = 240
	r := []rune(reason)
	if len(r) > maxRunes {
		reason = string(r[:maxRunes-1]) + "…"
	}
	return reason
}

func failureCategory(reason string) string {
	reason = strings.ToLower(reason)
	switch {
	case strings.Contains(reason, "login_required") || strings.Contains(reason, "login required"):
		return "login_required"
	case strings.Contains(reason, "checkpoint"):
		return "checkpoint_required"
	case strings.Contains(reason, "challenge"):
		return "challenge_required"
	case strings.Contains(reason, "cookie pool") || strings.Contains(reason, "auth circuit") || strings.Contains(reason, "cookie_missing"):
		return "cookie/auth unavailable"
	case strings.Contains(reason, "min_age_account") || strings.Contains(reason, "under 18") || strings.Contains(reason, "age restricted"):
		return "restricted/age"
	case strings.Contains(reason, "geoblock_required") || strings.Contains(reason, "geoblock") || strings.Contains(reason, "content restricted"):
		return "restricted/geoblock"
	case strings.Contains(reason, "timeout") || strings.Contains(reason, "deadline exceeded"):
		return "timeout"
	case strings.Contains(reason, "not found") || strings.Contains(reason, "unavailable") || strings.Contains(reason, "private"):
		return "private/unavailable"
	default:
		return "other"
	}
}

func isExpectedContentRestriction(category string) bool {
	return category == "restricted/geoblock" || category == "restricted/age"
}

func isOGCloudflareChallenge(reason string) bool {
	reason = strings.ToLower(reason)
	return strings.Contains(reason, "cloudflare") ||
		strings.Contains(reason, "cf-mitigated") ||
		strings.Contains(reason, "managed challenge") ||
		strings.Contains(reason, "http 403") ||
		strings.Contains(reason, "status 403") ||
		strings.Contains(reason, "403 forbidden")
}

func (m *Manager) recordLocalResolverFailure(reason string) {
	reason = strings.ToLower(reason)
	if strings.Contains(reason, "no video") || strings.Contains(reason, "no_video") || strings.Contains(reason, "no media") || strings.Contains(reason, "no_media") {
		m.localResolverNoVideo.Add(1)
	}
	if strings.Contains(reason, "not found") || strings.Contains(reason, "not_found") {
		m.localResolverNotFound.Add(1)
	}
}

func normalizeReason(reason string) string {
	reason = strings.ToLower(strings.TrimSpace(reason))
	if reason == "" {
		return "unknown"
	}
	var normalized strings.Builder
	lastUnderscore := false
	for _, char := range reason {
		isAlphaNumeric := char >= 'a' && char <= 'z' || char >= '0' && char <= '9'
		if isAlphaNumeric {
			normalized.WriteRune(char)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore && normalized.Len() > 0 {
			normalized.WriteByte('_')
			lastUnderscore = true
		}
	}
	value := strings.Trim(normalized.String(), "_")
	if value == "" {
		return "unknown"
	}
	if len(value) > 64 {
		value = strings.TrimRight(value[:64], "_")
	}
	return value
}

func safeDetailReason(reason string) string {
	raw := strings.TrimSpace(reason)
	lower := strings.ToLower(raw)
	if len(raw) > 80 ||
		strings.Contains(lower, "://") ||
		strings.ContainsAny(raw, "\r\n\t=&?") ||
		strings.Contains(lower, "authorization:") ||
		strings.Contains(lower, "cookie:") {
		return "other"
	}
	normalized := normalizeReason(raw)
	if len(normalized) > 48 {
		normalized = strings.TrimRight(normalized[:48], "_")
	}
	return normalized
}

func validPostID(postID string) bool {
	if len(postID) < 6 || len(postID) > 32 {
		return false
	}
	for _, char := range postID {
		if char >= 'a' && char <= 'z' ||
			char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' ||
			char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func addDailyDetail(details map[dailyDetailKey]uint64, postID, reason, client string) {
	if !validPostID(postID) {
		return
	}
	key := dailyDetailKey{
		postID: postID,
		reason: safeDetailReason(reason),
		client: safeDetailClient(client),
	}
	if _, exists := details[key]; !exists && len(details) >= maxDailyDetailEntries {
		return
	}
	details[key]++
}

func safeDetailClient(client string) string {
	switch client {
	case "telegram", "discord", "whatsapp", "bot", "browser":
		return client
	default:
		return ""
	}
}

func (m *Manager) failureDetailsLocked(minute int64, count int64) string {
	type groupedFailure struct {
		postID string
		reason string
		count  int
	}

	grouped := make([]groupedFailure, 0)
	indexes := make(map[string]int)
	for bucketMinute := minute - count + 1; bucketMinute <= minute; bucketMinute++ {
		b := m.buckets[bucketMinute%int64(len(m.buckets))]
		if b.minute <= minute-count || b.minute > minute {
			continue
		}
		for _, failure := range b.failures {
			key := failure.postID + "\x00" + failure.reason
			if index, ok := indexes[key]; ok {
				grouped[index].count++
				continue
			}
			indexes[key] = len(grouped)
			grouped = append(grouped, groupedFailure{postID: failure.postID, reason: failure.reason, count: 1})
		}
	}
	if len(grouped) == 0 {
		return ""
	}

	var details strings.Builder
	details.WriteString("\n\nFailures in current 15m window:")
	const maxDetails = 8
	for i, failure := range grouped {
		if i >= maxDetails {
			fmt.Fprintf(&details, "\n• …and %d more unique failures", len(grouped)-maxDetails)
			break
		}
		postID := failure.postID
		if postID == "" {
			postID = "unknown post"
		}
		if failure.count > 1 {
			fmt.Fprintf(&details, "\n• %s ×%d — %s", postID, failure.count, failure.reason)
		} else {
			fmt.Fprintf(&details, "\n• %s — %s", postID, failure.reason)
		}
	}
	return details.String()
}

func (m *Manager) windowLocked(minute int64, count int64) (s, f, restricted uint64) {
	for i := range m.buckets {
		b := m.buckets[i]
		if b.minute > minute-count && b.minute <= minute {
			s += b.success
			f += b.failure
			restricted += b.restricted
		}
	}
	return
}

func (m *Manager) RecordDBError(operation string, err error) {
	m.dbErrors.Add(1)
	slog.Error("database operation failed", "event", "db_error", "operation", operation, "err", err)
	m.mu.Lock()
	send := time.Since(m.lastDBAlert) >= 15*time.Minute
	if send {
		m.lastDBAlert = time.Now()
	}
	m.mu.Unlock()
	if send {
		m.Alert("🚨 InstaFix database error\nOperation: " + operation + "\nError: " + err.Error())
	}
}
func (m *Manager) RecordAuthHelperResult(success bool, postID, code string, err error) {
	now := time.Now().UTC()
	underlyingCode, cacheHit := authResultCode(code)
	m.mu.Lock()
	if success {
		m.authUsed.Add(1)
		detailReason := "upstream_success"
		if cacheHit {
			m.authCacheHits.Add(1)
			detailReason = "cache_hit"
		}
		addDailyDetail(m.authDetails, postID, detailReason, "")
		wasActive := m.authAlertActive
		m.authFailureStreak = 0
		m.authAlertActive = false
		m.mu.Unlock()
		if wasActive {
			m.Alert("✅ InstaFix Instagram cookies recovered\nAuthenticated fallback succeeded again.")
		}
		return
	}
	m.authFailed.Add(1)
	detailPrefix := "upstream_failed_"
	if cacheHit {
		m.authCachedFailures.Add(1)
		detailPrefix = "cache_failed_"
	}
	addDailyDetail(m.authDetails, postID, detailPrefix+underlyingCode, "")
	m.mu.Unlock()
	m.recordAuthHelperFailure(underlyingCode, err)

	if !authSessionError(underlyingCode) {
		return
	}
	m.authSessionFailures.Add(1)
	if strings.Contains(underlyingCode, "cookie") || strings.Contains(underlyingCode, "auth_circuit") {
		m.authCookiePoolFailures.Add(1)
	}

	m.mu.Lock()
	m.authFailureStreak++
	shouldAlert := m.authFailureStreak >= 3 && (!m.authAlertActive || now.Sub(m.lastAuthAlert) >= time.Hour)
	if shouldAlert {
		m.authAlertActive = true
		m.lastAuthAlert = now
	}
	streak := m.authFailureStreak
	m.mu.Unlock()
	if shouldAlert {
		msg := fmt.Sprintf("🚨 InstaFix Instagram cookies likely need update\n\nAuthenticated fallback failed %d times in a row.\nLast post: %s\nError code: %s", streak, postID, underlyingCode)
		if err != nil {
			msg += "\nError: " + err.Error()
		}
		msg += authCookieStatus()
		msg += "\n\nA private recovery browser will be prepared automatically."
		msg += "\nManual fallback: /root/update-instagram-cookie.sh"
		requestCookieRecovery(postID, underlyingCode)
		m.Alert(msg)
	}
}

func authResultCode(code string) (underlying string, cacheHit bool) {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "cache_hit" {
		return "success", true
	}
	if strings.HasPrefix(code, "cache_hit:") {
		code = strings.TrimSpace(strings.TrimPrefix(code, "cache_hit:"))
		cacheHit = true
	}
	return normalizeReason(code), cacheHit
}

func (m *Manager) RecordAuthHelperSkipped(postID, reason string) {
	m.authSkipped.Add(1)
	m.mu.Lock()
	addDailyDetail(m.authDetails, postID, "skipped_"+safeDetailReason(reason), "")
	m.mu.Unlock()
}

func requestCookieRecovery(postID, code string) {
	path := strings.TrimSpace(os.Getenv("COOKIE_RECOVERY_REQUEST_FILE"))
	if path == "" {
		return
	}
	payload, err := json.Marshal(struct {
		RequestedAt string `json:"requested_at"`
		PostID      string `json:"post_id"`
		Code        string `json:"code"`
	}{
		RequestedAt: time.Now().UTC().Format(time.RFC3339),
		PostID:      postID,
		Code:        code,
	})
	if err != nil {
		return
	}
	if err := os.WriteFile(path, append(payload, '\n'), 0600); err != nil {
		slog.Warn("cookie recovery request write failed", "path", path, "err", err)
	}
}

func (m *Manager) recordAuthHelperFailure(code string, err error) {
	code = strings.ToLower(strings.TrimSpace(code))
	reason := code
	if err != nil {
		reason += " " + strings.ToLower(err.Error())
	}
	switch code {
	case "auth_helper_unreachable", "auth_circuit_open", "cookie_missing", "cookie_dir_missing":
		m.authFallbackUnavailable.Add(1)
	}
	if code == "login_required" || code == "require_login" || strings.Contains(reason, "accounts/login") || strings.Contains(reason, "login redirect") {
		m.authLoginRedirect.Add(1)
	}
}

func authCookieStatus() string {
	base := strings.TrimSpace(os.Getenv("AUTH_HELPER_URL"))
	if base == "" {
		return ""
	}
	if !strings.HasPrefix(base, "http://127.0.0.1") && !strings.HasPrefix(base, "http://localhost") {
		return ""
	}
	client := http.Client{Timeout: 2 * time.Second}
	res, err := client.Get(strings.TrimRight(base, "/") + "/healthz")
	if err != nil {
		return "\n\nCookie pool: unavailable (healthz error: " + conciseError(err) + ")"
	}
	defer res.Body.Close()
	var payload struct {
		CookiePool struct {
			Total       int `json:"total"`
			Available   int `json:"available"`
			CoolingDown int `json:"cooling_down"`
			NeedsLogin  int `json:"needs_login"`
		} `json:"cookie_pool"`
		CircuitOpen      bool   `json:"auth_circuit_open"`
		CircuitRemaining int    `json:"auth_circuit_remaining_seconds"`
		CircuitReason    string `json:"auth_circuit_reason"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 32*1024)).Decode(&payload); err != nil {
		return "\n\nCookie pool: unavailable (invalid healthz)"
	}
	line := fmt.Sprintf("\n\nCookie pool: %d/%d available, %d cooling down, %d need login", payload.CookiePool.Available, payload.CookiePool.Total, payload.CookiePool.CoolingDown, payload.CookiePool.NeedsLogin)
	if payload.CircuitOpen {
		line += fmt.Sprintf("\nAuth circuit: open for %ds", payload.CircuitRemaining)
		if payload.CircuitReason != "" {
			line += " after " + payload.CircuitReason
		}
	} else {
		line += "\nAuth circuit: closed"
	}
	return line
}

func authSessionError(code string) bool {
	switch strings.TrimSpace(code) {
	case "login_required", "checkpoint_required", "challenge_required", "auth_forbidden", "cookie_missing", "session_invalid":
		return true
	default:
		return false
	}
}
func (m *Manager) Alert(text string) {
	if m.telegram != nil {
		m.telegram.Send(text)
	}
}
func (m *Manager) AlertStartup() {
	m.Alert("🚀 InstaFix test instance started/restarted\nHost: " + hostname() + "\nTime: " + time.Now().UTC().Format(time.RFC3339))
}
func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}

func (m *Manager) Run(ctx context.Context) {
	go m.telegram.Run(ctx)
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.sampleRuntime()
			}
		}
	}()
	go func() {
		for {
			now := time.Now().UTC()
			next := time.Date(now.Year(), now.Month(), now.Day(), m.reportHour, 5, 0, 0, time.UTC)
			if !next.After(now) {
				next = next.Add(24 * time.Hour)
			}
			for {
				t := time.NewTimer(time.Until(next))
				select {
				case <-ctx.Done():
					t.Stop()
					return
				case <-t.C:
					if err := m.SendDailyReport(ctx); err != nil {
						next = time.Now().UTC().Add(dailyReportRetryDelay)
						slog.Error("Daily Telegram report delivery failed; counters retained", "err", err, "retry_at", next)
						continue
					}
				}
				break
			}
		}
	}()
}

// SendDailyReport synchronously delivers the current report and resets daily
// counters only after Telegram confirms the message.
func (m *Manager) SendDailyReport(ctx context.Context) error {
	if err := m.SendReportPreview(ctx); err != nil {
		return err
	}
	m.resetDaily()
	return nil
}

// SendReportPreview delivers the current report without resetting counters.
// It is used for authenticated production checks of the real in-memory data.
func (m *Manager) SendReportPreview(ctx context.Context) error {
	if m == nil || m.telegram == nil {
		return ErrTelegramDisabled
	}
	return m.telegram.SendHTMLWithRetry(ctx, m.DailyReport())
}

func (m *Manager) resetDaily() {
	m.requests.Store(0)
	m.cacheHits.Store(0)
	m.scrapeSuccess.Store(0)
	m.scrapeFailure.Store(0)
	m.dbErrors.Store(0)
	m.previewRequests.Store(0)
	m.previewFull.Store(0)
	m.previewFallback.Store(0)
	m.previewFailed.Store(0)
	m.previewVideos.Store(0)
	m.previewImages.Store(0)
	m.previewGeneric.Store(0)
	m.previewTelegram.Store(0)
	m.previewDiscord.Store(0)
	m.previewWhatsApp.Store(0)
	m.previewTelegramFull.Store(0)
	m.previewTelegramFallback.Store(0)
	m.previewTelegramFailed.Store(0)
	m.previewDiscordFull.Store(0)
	m.previewDiscordFallback.Store(0)
	m.previewDiscordFailed.Store(0)
	m.previewWhatsAppFull.Store(0)
	m.previewWhatsAppFallback.Store(0)
	m.previewWhatsAppFailed.Store(0)
	m.previewMessengerVideos.Store(0)
	m.previewMessengerImages.Store(0)
	m.telegramVideoDirect.Store(0)
	m.telegramVideoCompact.Store(0)
	m.telegramVideoExpectedImage.Store(0)
	m.telegramVideoBlocked.Store(0)
	m.previewBrowsers.Store(0)
	m.previewBots.Store(0)
	m.ogProxyServed.Store(0)
	m.ogProxyFallback.Store(0)
	m.ogProxyUpstreamFailures.Store(0)
	m.ogProxyCloudflare403.Store(0)
	m.ogProxyOtherFailures.Store(0)
	m.ogClientRedirects.Store(0)
	m.mediaStreamHead.Store(0)
	m.mediaStreamGet.Store(0)
	m.mediaStreamRange.Store(0)
	m.mediaStream200.Store(0)
	m.mediaStream206.Store(0)
	m.mediaStreamFailures.Store(0)
	m.mediaStreamBytes.Store(0)
	m.largestMediaStreamBytes.Store(0)
	m.peakMediaStreams.Store(m.activeMediaStreams.Load())
	m.peakRuntimeBytes.Store(0)
	m.peakGoroutines.Store(0)
	m.authUsed.Store(0)
	m.authCacheHits.Store(0)
	m.authFailed.Store(0)
	m.authCachedFailures.Store(0)
	m.authSkipped.Store(0)
	m.authSessionFailures.Store(0)
	m.authCookiePoolFailures.Store(0)
	m.authFallbackUnavailable.Store(0)
	m.authLoginRedirect.Store(0)
	m.metadataProxyRequests.Store(0)
	m.metadataProxySuccess.Store(0)
	m.metadataProxyFailures.Store(0)
	m.metadataProxyUploadBytes.Store(0)
	m.metadataProxyDownloadBytes.Store(0)
	m.localResolverNoVideo.Store(0)
	m.localResolverNotFound.Store(0)
	for i := range m.status {
		m.status[i].Store(0)
	}
	m.mu.Lock()
	clear(m.users)
	clear(m.posts)
	clear(m.previewPosts)
	clear(m.previewFallbackPosts)
	clear(m.previewFailedPosts)
	clear(m.failureReasons)
	clear(m.ogProxyFallbackReasons)
	clear(m.previewFallbackDetails)
	clear(m.previewFailedDetails)
	clear(m.authDetails)
	clear(m.ogClientRedirectDetails)
	clear(m.telegramVideoDetails)
	m.day = time.Now().UTC().Format("2006-01-02")
	m.requestMinute = time.Now().UTC().Unix() / 60
	m.requestsThisMinute = 0
	m.peakRequestsPerMinute = 0
	m.mu.Unlock()
	m.sampleRuntime()
}

func (m *Manager) DailyReport() string {
	m.mu.Lock()
	users, posts, previewPosts := estimate(m.users), estimate(m.posts), estimate(m.previewPosts)
	fallbackPosts := estimate(m.previewFallbackPosts)
	failedPosts := estimate(m.previewFailedPosts)
	failures := m.topFailureReasonsLocked(4)
	ogFallbackReasons := topReasons(m.ogProxyFallbackReasons, 4)
	fallbackDetails := topDailyDetails(m.previewFallbackDetails, 3)
	failedDetails := topDailyDetails(m.previewFailedDetails, 3)
	authDetails := topDailyDetails(m.authDetails, 4)
	ogRedirectDetails := topDailyDetails(m.ogClientRedirectDetails, 3)
	telegramVideoDetails := topTelegramVideoDetails(m.telegramVideoDetails, 8)
	day := m.day
	peakRequestsPerMinute := m.peakRequestsPerMinute
	m.mu.Unlock()

	total := m.scrapeSuccess.Load() + m.scrapeFailure.Load()
	previewTotal := m.previewRequests.Load()
	previewFull := m.previewFull.Load()
	previewRate := Percent(previewFull, previewTotal)
	messengerFull := m.previewTelegramFull.Load() + m.previewDiscordFull.Load() + m.previewWhatsAppFull.Load()
	messengerFallback := m.previewTelegramFallback.Load() + m.previewDiscordFallback.Load() + m.previewWhatsAppFallback.Load()
	messengerFailed := m.previewTelegramFailed.Load() + m.previewDiscordFailed.Load() + m.previewWhatsAppFailed.Load()
	messengerTotal := messengerFull + messengerFallback + messengerFailed
	messengerRate := Percent(messengerFull, messengerTotal)
	telegramVideoDirect := m.telegramVideoDirect.Load()
	telegramVideoCompact := m.telegramVideoCompact.Load()
	telegramVideoExpectedImage := m.telegramVideoExpectedImage.Load()
	telegramVideoBlocked := m.telegramVideoBlocked.Load()
	telegramVideoTotal := telegramVideoDirect + telegramVideoCompact + telegramVideoExpectedImage + telegramVideoBlocked
	otherPreviewTotal := previewTotal - min(previewTotal, messengerTotal)
	otherPreviewFallback := m.previewFallback.Load() - min(m.previewFallback.Load(), messengerFallback)
	otherPreviewFailed := m.previewFailed.Load() - min(m.previewFailed.Load(), messengerFailed)

	authSuccess := m.authUsed.Load()
	authCache := m.authCacheHits.Load()
	authUpstream := authSuccess
	if authCache < authUpstream {
		authUpstream -= authCache
	} else {
		authUpstream = 0
	}
	authFailed := m.authFailed.Load()
	authCachedFailed := m.authCachedFailures.Load()
	authUpstreamFailed := authFailed
	if authCachedFailed < authUpstreamFailed {
		authUpstreamFailed -= authCachedFailed
	} else {
		authUpstreamFailed = 0
	}
	authSkipped := m.authSkipped.Load()
	authSessionFailures := m.authSessionFailures.Load()
	authActivity := authSuccess + authFailed + authSkipped + authSessionFailures +
		m.authCookiePoolFailures.Load() + m.authFallbackUnavailable.Load() + m.authLoginRedirect.Load()

	ogRedirects := m.ogClientRedirects.Load()
	ogServed := m.ogProxyServed.Load()
	ogFallback := m.ogProxyFallback.Load()
	ogActivity := ogRedirects + ogServed + ogFallback + m.ogProxyUpstreamFailures.Load() +
		m.ogProxyCloudflare403.Load() + m.ogProxyOtherFailures.Load()

	metadataActivity := m.metadataProxyRequests.Load() + m.metadataProxySuccess.Load() +
		m.metadataProxyFailures.Load() + m.metadataProxyUploadBytes.Load() + m.metadataProxyDownloadBytes.Load()
	uptime := time.Since(m.started).Round(time.Minute)

	var technical strings.Builder
	technical.WriteString("🔧 <b>Technical details</b>")
	fmt.Fprintf(&technical, "\n\n🎯 <b>Previews</b>\nRendered <code>%s</code> · Unique <code>~%s</code> · Full rate <code>%.1f%%</code>\nFull <code>%s</code> · Generic fallback requests <code>%s</code> · Failed <code>%s</code>\nAdvertised: 🎬 <code>%s</code> · 🖼 <code>%s</code> · Generic <code>%s</code>",
		formatCount(previewTotal), formatCount(uint64(previewPosts)), previewRate,
		formatCount(previewFull), formatCount(m.previewFallback.Load()), formatCount(m.previewFailed.Load()),
		formatCount(m.previewVideos.Load()), formatCount(m.previewImages.Load()), formatCount(m.previewGeneric.Load()))
	if m.previewFallback.Load() > 0 {
		fmt.Fprintf(&technical, "\nFallback unique posts <code>~%s</code> · Top %s", formatCount(uint64(fallbackPosts)), fallbackDetails)
	}
	if m.previewFailed.Load() > 0 {
		fmt.Fprintf(&technical, "\nFailed unique posts <code>~%s</code> · Top %s", formatCount(uint64(failedPosts)), failedDetails)
	}

	fmt.Fprintf(&technical, "\n\n📡 <b>Media streams</b>\nHEAD <code>%s</code> · GET <code>%s</code> · Range <code>%s</code>\nHTTP 200 <code>%s</code> · 206 <code>%s</code> · Failed <code>%s</code>\nTraffic <code>%s</code> · Largest <code>%s</code>",
		formatCount(m.mediaStreamHead.Load()), formatCount(m.mediaStreamGet.Load()), formatCount(m.mediaStreamRange.Load()),
		formatCount(m.mediaStream200.Load()), formatCount(m.mediaStream206.Load()), formatCount(m.mediaStreamFailures.Load()),
		formatBytes(m.mediaStreamBytes.Load()), formatBytes(m.largestMediaStreamBytes.Load()))

	fmt.Fprintf(&technical, "\n\n🤖 <b>Clients</b>\nTelegram <code>%s</code> · Discord <code>%s</code>\nWhatsApp <code>%s</code> · Browsers <code>%s</code> · Other bots <code>%s</code>",
		formatCount(m.previewTelegram.Load()), formatCount(m.previewDiscord.Load()),
		formatCount(m.previewWhatsApp.Load()), formatCount(m.previewBrowsers.Load()), formatCount(m.previewBots.Load()))

	fmt.Fprintf(&technical, "\n\n🔎 <b>Resolver &amp; cache</b>\nFresh <code>%s</code> · Successful <code>%s</code> · Failed <code>%s</code>\nCache hits <code>%s</code>\nNo video <code>%s</code> · Not found <code>%s</code>\nFailures <code>%s</code>",
		formatCount(total), formatCount(m.scrapeSuccess.Load()), formatCount(m.scrapeFailure.Load()),
		formatCount(m.cacheHits.Load()),
		formatCount(m.localResolverNoVideo.Load()), formatCount(m.localResolverNotFound.Load()), failures)

	if metadataActivity > 0 {
		fmt.Fprintf(&technical, "\n\n🌐 <b>Residential metadata proxy</b>\nRequests <code>%s</code> · Success <code>%s</code> · Failed <code>%s</code>\nPayload up <code>%s</code> · down <code>%s</code>",
			formatCount(m.metadataProxyRequests.Load()), formatCount(m.metadataProxySuccess.Load()), formatCount(m.metadataProxyFailures.Load()),
			formatBytes(m.metadataProxyUploadBytes.Load()), formatBytes(m.metadataProxyDownloadBytes.Load()))
	}

	if authActivity > 0 {
		fmt.Fprintf(&technical, "\n\n🍪 <b>Auth helper</b>\nSuccess <code>%s</code>: upstream <code>%s</code> · cache <code>%s</code>\nFailed <code>%s</code>: upstream <code>%s</code> · cache <code>%s</code>\nSkipped without auth request <code>%s</code> · Session failures <code>%s</code>\nPool failures <code>%s</code> · Unavailable <code>%s</code> · Login redirects <code>%s</code>\nTop %s",
			formatCount(authSuccess), formatCount(authUpstream), formatCount(authCache),
			formatCount(authFailed), formatCount(authUpstreamFailed), formatCount(authCachedFailed),
			formatCount(authSkipped), formatCount(authSessionFailures),
			formatCount(m.authCookiePoolFailures.Load()), formatCount(m.authFallbackUnavailable.Load()), formatCount(m.authLoginRedirect.Load()),
			authDetails)
	}

	if ogActivity > 0 {
		fmt.Fprintf(&technical, "\n\n🧯 <b>OGInstagram</b>\nCross-domain redirects <code>%s</code> · Server proxy served <code>%s</code>\nProxy fallback to local <code>%s</code> · Upstream failures <code>%s</code>\nCF 403 <code>%s</code> · Other <code>%s</code>\nRedirects %s\nProxy reasons <code>%s</code>",
			formatCount(ogRedirects), formatCount(ogServed), formatCount(ogFallback),
			formatCount(m.ogProxyUpstreamFailures.Load()), formatCount(m.ogProxyCloudflare403.Load()), formatCount(m.ogProxyOtherFailures.Load()),
			ogRedirectDetails, ogFallbackReasons)
	}

	fmt.Fprintf(&technical, "\n\n⚙️ <b>Runtime</b>\nHTTP <code>%s</code> · 2xx <code>%s</code> · 3xx <code>%s</code> · 4xx <code>%s</code> · 5xx <code>%s</code>\nUnique IPs <code>~%s</code> · Post IDs <code>~%s</code> · DB errors <code>%s</code>\nPeak <code>%s req/min</code> · <code>%s streams</code>\nGo memory <code>%s</code> · Goroutines <code>%s</code>\nUptime <code>%s</code>",
		formatCount(m.requests.Load()), formatCount(m.status[2].Load()), formatCount(m.status[3].Load()), formatCount(m.status[4].Load()), formatCount(m.status[5].Load()),
		formatCount(uint64(users)), formatCount(uint64(posts)), formatCount(m.dbErrors.Load()),
		formatCount(peakRequestsPerMinute), formatCount(uint64(max(0, m.peakMediaStreams.Load()))),
		formatBytes(m.peakRuntimeBytes.Load()), formatCount(m.peakGoroutines.Load()), uptime)

	var telegramVideoSummary strings.Builder
	if telegramVideoTotal > 0 {
		fmt.Fprintf(&telegramVideoSummary, "\n🎞 <b>Telegram Reels:</b> direct %s · compact %s · expected image %s · blocked %s",
			formatCount(telegramVideoDirect), formatCount(telegramVideoCompact), formatCount(telegramVideoExpectedImage), formatCount(telegramVideoBlocked))
		if telegramVideoDetails != "none" {
			fmt.Fprintf(&telegramVideoSummary, "\n<blockquote expandable>🎞 <b>Reel details</b>\n%s</blockquote>", telegramVideoDetails)
		}
	}

	var summaryExtra strings.Builder
	if authActivity > 0 {
		fmt.Fprintf(&summaryExtra, "\n🍪 Auth helper: %s upstream success · %s upstream failed · %s cached results\n   %s skipped without auth request",
			formatCount(authUpstream), formatCount(authUpstreamFailed), formatCount(authCache+authCachedFailed), formatCount(authSkipped))
	}
	if ogActivity > 0 {
		fmt.Fprintf(&summaryExtra, "\n🌐 OGInstagram: %s redirects · %s proxy served · %s proxy fallback", formatCount(ogRedirects), formatCount(ogServed), formatCount(ogFallback))
	}

	report := fmt.Sprintf("📊 <b>Instagram7 daily report — %s UTC</b>\n\n✅ <b>Messenger full previews: %s/%s</b> (%.1f%%)\n🎬 Messenger videos: <b>%s</b> · 🖼 Images: %s\n⚠️ Messenger fallbacks: <b>%s</b> · Failed: %s%s\n🔗 Unique Instagram posts across all clients: ≈%s\n🕷 Other crawler/browser requests: %s · Fallback: %s · Failed: %s\n\n📦 Video served: <b>%s</b> across %s GET streams\n⚡ Peak load: %s HTTP requests/min · %s concurrent streams\n🧠 Peak Go memory: %s\n\n🧩 Local resolver: %s/%s successful scrapes · %s cache hits%s\n\n<blockquote expandable>%s</blockquote>",
		day,
		formatCount(messengerFull), formatCount(messengerTotal), messengerRate,
		formatCount(m.previewMessengerVideos.Load()), formatCount(m.previewMessengerImages.Load()),
		formatCount(messengerFallback), formatCount(messengerFailed), telegramVideoSummary.String(), formatCount(uint64(previewPosts)),
		formatCount(otherPreviewTotal), formatCount(otherPreviewFallback), formatCount(otherPreviewFailed),
		formatBytes(m.mediaStreamBytes.Load()), formatCount(m.mediaStreamGet.Load()),
		formatCount(peakRequestsPerMinute), formatCount(uint64(max(0, m.peakMediaStreams.Load()))), formatBytes(m.peakRuntimeBytes.Load()),
		formatCount(m.scrapeSuccess.Load()), formatCount(total), formatCount(m.cacheHits.Load()),
		summaryExtra.String(), technical.String(),
	)
	return limitDailyReport(report)
}

func telegramVideoReasonLabel(reason string) string {
	switch reason {
	case "within_20_mib":
		return "≤20 MiB"
	case "oversized_over_20_mib":
		return ">20 MiB"
	case "oversized_compact_unavailable":
		return ">20 MiB · compact unavailable"
	case "age_restricted_21":
		return "21+ restriction"
	case "region_restricted":
		return "region restriction"
	case "restricted":
		return "Instagram restriction"
	case "not_found_private_or_deleted":
		return "private/deleted/not found"
	case "expired_media_url":
		return "expired media URL"
	case "rate_limited":
		return "Instagram rate limit"
	case "upstream_timeout":
		return "Instagram timeout"
	case "upstream_5xx":
		return "Instagram 5xx"
	case "reel_resolved_as_image":
		return "Reel resolved as image"
	case "size_unknown":
		return "size unknown"
	case "no_renderable_media":
		return "no renderable media"
	case "invalid_media_index", "media_index_out_of_range":
		return "invalid media index"
	default:
		return strings.ReplaceAll(reason, "_", " ")
	}
}

func topTelegramVideoDetails(details map[string]dailyTelegramVideo, limit int) string {
	if len(details) == 0 || limit <= 0 {
		return "none"
	}
	type item struct {
		postID string
		detail dailyTelegramVideo
	}
	items := make([]item, 0, len(details))
	for postID, detail := range details {
		if detail.Requests > 0 || detail.Delivery != "" {
			items = append(items, item{postID: postID, detail: detail})
		}
	}
	priority := func(decision string) int {
		switch decision {
		case "blocked":
			return 0
		case "expected_image":
			return 1
		case "compact":
			return 2
		default:
			return 3
		}
	}
	sort.Slice(items, func(i, j int) bool {
		pi, pj := priority(items[i].detail.Decision), priority(items[j].detail.Decision)
		if pi != pj {
			return pi < pj
		}
		if items[i].detail.Requests != items[j].detail.Requests {
			return items[i].detail.Requests > items[j].detail.Requests
		}
		return items[i].postID < items[j].postID
	})
	if len(items) > limit {
		items = items[:limit]
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		d := item.detail
		label := "✅ direct"
		switch d.Decision {
		case "compact":
			label = "🗜 compact"
		case "expected_image":
			label = "🖼 expected image"
		case "blocked":
			label = "🚫 blocked"
		}
		line := fmt.Sprintf("• <a href=\"https://www.instagram.com/reel/%s/\">%s</a> — %s", item.postID, item.postID, label)
		if d.OriginalBytes > 0 {
			line += " · original " + formatBytes(uint64(d.OriginalBytes))
		}
		if d.Reason != "" && d.Reason != "unknown" {
			line += " · " + telegramVideoReasonLabel(d.Reason)
		}
		if d.Delivery != "" && d.Delivery != "unknown" {
			line += " → " + d.Delivery
			if d.DeliveredBytes > 0 {
				line += " " + formatBytes(uint64(d.DeliveredBytes))
			}
			if d.DeliveryReason != "" && d.DeliveryReason != "unknown" {
				line += " (" + d.DeliveryReason + ")"
			}
		}
		if d.Requests > 1 {
			line += " ×" + formatCount(d.Requests)
		}
		parts = append(parts, line)
	}
	return strings.Join(parts, "\n")
}

func topDailyDetails(details map[dailyDetailKey]uint64, limit int) string {
	if len(details) == 0 || limit <= 0 {
		return "none"
	}
	type item struct {
		key   dailyDetailKey
		count uint64
	}
	items := make([]item, 0, len(details))
	for key, count := range details {
		if count > 0 {
			items = append(items, item{key: key, count: count})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].count != items[j].count {
			return items[i].count > items[j].count
		}
		if items[i].key.postID != items[j].key.postID {
			return items[i].key.postID < items[j].key.postID
		}
		if items[i].key.reason != items[j].key.reason {
			return items[i].key.reason < items[j].key.reason
		}
		return items[i].key.client < items[j].key.client
	})
	if len(items) > limit {
		items = items[:limit]
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		detail := fmt.Sprintf("<code>%s</code> %s", item.key.postID, item.key.reason)
		if item.key.client != "" {
			detail += "/" + item.key.client
		}
		if item.count > 1 {
			detail += " ×" + formatCount(item.count)
		}
		parts = append(parts, detail)
	}
	return strings.Join(parts, " | ")
}

func limitDailyReport(report string) string {
	runes := []rune(report)
	if len(runes) <= maxDailyReportRunes {
		return report
	}
	const suffix = "\n…\n</blockquote>"
	suffixRunes := []rune(suffix)
	limit := maxDailyReportRunes - len(suffixRunes)
	if limit <= 0 {
		return string(runes[:maxDailyReportRunes])
	}
	cut := limit
	for cut > 0 && runes[cut-1] != '\n' {
		cut--
	}
	if cut == 0 {
		return string(runes[:limit]) + suffix
	}
	return string(runes[:cut-1]) + suffix
}

func formatCount(value uint64) string {
	raw := strconv.FormatUint(value, 10)
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
			formatted.WriteByte(' ')
		}
		formatted.WriteString(raw[i : i+3])
	}
	return formatted.String()
}

func formatBytes(value uint64) string {
	const unit = 1000
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}
	divisor := float64(unit)
	units := []string{"KB", "MB", "GB", "TB"}
	for _, suffix := range units {
		if float64(value) < divisor*unit || suffix == units[len(units)-1] {
			return fmt.Sprintf("%.1f %s", float64(value)/divisor, suffix)
		}
		divisor *= unit
	}
	return fmt.Sprintf("%d B", value)
}

func (m *Manager) topFailureReasonsLocked(limit int) string {
	return topReasons(m.failureReasons, limit)
}

func topReasons(reasons map[string]uint64, limit int) string {
	if len(reasons) == 0 {
		return "none"
	}
	type pair struct {
		reason string
		count  uint64
	}
	items := make([]pair, 0, len(reasons))
	for reason, count := range reasons {
		if count > 0 {
			items = append(items, pair{reason: reason, count: count})
		}
	}
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if items[j].count > items[i].count {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
	if len(items) > limit {
		items = items[:limit]
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, fmt.Sprintf("%s: %d", item.reason, item.count))
	}
	return strings.Join(parts, " | ")
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (w *responseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *responseWriter) WriteHeader(code int) {
	if w.status != 0 {
		return
	}
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
func (w *responseWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

func (m *Manager) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w}
		next.ServeHTTP(rw, r)
		code := rw.status
		if code == 0 {
			code = 200
		}
		m.requests.Add(1)
		if code/100 >= 0 && code/100 < len(m.status) {
			m.status[code/100].Add(1)
		}
		m.mu.Lock()
		minute := start.UTC().Unix() / 60
		if m.requestMinute != minute {
			m.requestMinute = minute
			m.requestsThisMinute = 0
		}
		m.requestsThisMinute++
		if m.requestsThisMinute > m.peakRequestsPerMinute {
			m.peakRequestsPerMinute = m.requestsThisMinute
		}
		if ip := clientIP(r); ip != "" {
			addEstimate(m.users, m.day+":"+ip)
		}
		if p := postID(r.URL.Path); p != "" {
			addEstimate(m.posts, p)
		}
		m.mu.Unlock()
		slog.Info("request completed", "event", "request", "method", r.Method, "path", r.URL.Path, "status", code, "duration_ms", time.Since(start).Milliseconds(), "client", clientClass(r.UserAgent()))
	})
}

func clientIP(r *http.Request) string {
	v := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0])
	if net.ParseIP(v) != nil {
		return v
	}
	v = strings.TrimSpace(r.Header.Get("X-Real-IP"))
	if net.ParseIP(v) != nil {
		return v
	}
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return h
	}
	return r.RemoteAddr
}
func postID(path string) string {
	for _, v := range strings.Split(strings.Trim(path, "/"), "/") {
		if len(v) >= 6 && (v[0] == 'B' || v[0] == 'C' || v[0] == 'D') {
			return v
		}
	}
	return ""
}
func clientClass(ua string) string {
	u := strings.ToLower(ua)
	switch {
	case strings.Contains(u, "telegrambot"):
		return "telegram"
	case strings.Contains(u, "discordbot"):
		return "discord"
	case strings.Contains(u, "whatsapp"):
		return "whatsapp"
	case utils.IsBot(ua):
		return "bot"
	default:
		return "browser"
	}
}

func requestClientClass(r *http.Request) string {
	if r == nil {
		return "browser"
	}
	return clientClass(r.UserAgent())
}

func EnvReportHour() int { n, _ := strconv.Atoi(os.Getenv("DAILY_REPORT_HOUR_UTC")); return n }
func Percent(n, d uint64) float64 {
	if d == 0 {
		return 0
	}
	return math.Round(1000*float64(n)/float64(d)) / 10
}
