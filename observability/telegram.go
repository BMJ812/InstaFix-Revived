package observability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultTelegramAPIBase     = "https://api.telegram.org"
	defaultTelegramMaxAttempts = 3
	defaultTelegramRetryDelay  = 250 * time.Millisecond
	defaultTelegramMaxDelay    = 5 * time.Second
)

var ErrTelegramDisabled = errors.New("Telegram delivery disabled: TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID are required")

type telegramHTTPError struct {
	status     string
	body       string
	retryAfter time.Duration
	transient  bool
}

type telegramSendResponse struct {
	Result struct {
		Entities []struct {
			Type string `json:"type"`
		} `json:"entities"`
	} `json:"result"`
}

func (e *telegramHTTPError) Error() string {
	return fmt.Sprintf("Telegram status %s: %s", e.status, e.body)
}

type TelegramClient struct {
	token       string
	chat        string
	client      *http.Client
	queue       chan string
	apiBase     string
	maxAttempts int
	retryDelay  time.Duration
	maxDelay    time.Duration
}

func NewTelegramClient(token, chat string) *TelegramClient {
	return &TelegramClient{
		token:       strings.TrimSpace(token),
		chat:        strings.TrimSpace(chat),
		client:      &http.Client{Timeout: 10 * time.Second},
		queue:       make(chan string, 64),
		apiBase:     defaultTelegramAPIBase,
		maxAttempts: defaultTelegramMaxAttempts,
		retryDelay:  defaultTelegramRetryDelay,
		maxDelay:    defaultTelegramMaxDelay,
	}
}

func (c *TelegramClient) enabled() bool {
	return c != nil && c.token != "" && c.chat != ""
}

func (c *TelegramClient) logConfigState() {
	if c == nil || !c.enabled() {
		slog.Warn(
			"Telegram reports and alerts are disabled: missing configuration",
			"missing_token", c == nil || c.token == "",
			"missing_chat_id", c == nil || c.chat == "",
		)
		return
	}
	slog.Info("Telegram reports and alerts enabled")
}

// Send queues an alert without blocking request handling.
func (c *TelegramClient) Send(text string) {
	if c == nil || !c.enabled() {
		return
	}
	text = truncateTelegramText(text)
	select {
	case c.queue <- text:
	default:
		slog.Warn("Telegram queue full; alert dropped")
	}
}

func (c *TelegramClient) Run(ctx context.Context) {
	if c == nil {
		return
	}
	c.logConfigState()
	if !c.enabled() {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case text := <-c.queue:
			if err := c.SendWithRetry(ctx, text); err != nil {
				slog.Error("Telegram alert delivery failed", "err", err)
			}
		}
	}
}

// SendWithRetry synchronously confirms Telegram delivery. It retries only
// transient network errors, rate limits, and server errors.
func (c *TelegramClient) SendWithRetry(ctx context.Context, text string) error {
	return c.sendWithRetry(ctx, text, "")
}

// SendHTMLWithRetry is reserved for trusted, escaped report templates. Alerts
// remain plain text so arbitrary error details cannot break Telegram parsing.
func (c *TelegramClient) SendHTMLWithRetry(ctx context.Context, text string) error {
	return c.sendWithRetry(ctx, text, "HTML")
}

func (c *TelegramClient) sendWithRetry(ctx context.Context, text, parseMode string) error {
	if c == nil || !c.enabled() {
		return ErrTelegramDisabled
	}
	text = truncateTelegramText(text)
	attempts := c.maxAttempts
	if attempts < 1 {
		attempts = 1
	}
	delay := c.retryDelay
	if delay < 0 {
		delay = 0
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		retryAfter, transient, err := c.sendOnce(ctx, text, parseMode)
		if err == nil {
			return nil
		}
		lastErr = err
		if !transient || attempt == attempts {
			break
		}
		wait := delay
		for i := 1; i < attempt; i++ {
			wait *= 2
		}
		if retryAfter > wait {
			wait = retryAfter
		}
		if c.maxDelay > 0 && wait > c.maxDelay {
			wait = c.maxDelay
		}
		slog.Warn("Telegram delivery failed; retrying", "attempt", attempt, "max_attempts", attempts, "retry_in", wait, "err", err)
		if err := waitForRetry(ctx, wait); err != nil {
			return err
		}
	}
	return fmt.Errorf("Telegram delivery failed after %d attempt(s): %w", attempts, lastErr)
}

func (c *TelegramClient) sendOnce(ctx context.Context, text, parseMode string) (time.Duration, bool, error) {
	form := url.Values{"chat_id": {c.chat}, "text": {text}}
	if parseMode != "" {
		form.Set("parse_mode", parseMode)
	}
	endpoint := strings.TrimRight(c.apiBase, "/") + "/bot" + c.token + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, false, ctx.Err()
		}
		return 0, true, err
	}
	defer res.Body.Close()
	if res.StatusCode/100 == 2 {
		if parseMode != "" {
			body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
			var response telegramSendResponse
			if json.Unmarshal(body, &response) == nil {
				entityTypes := make([]string, 0, len(response.Result.Entities))
				for _, entity := range response.Result.Entities {
					entityTypes = append(entityTypes, entity.Type)
				}
				slog.Info("Telegram HTML message accepted", "entities", strings.Join(entityTypes, ","))
			}
		}
		return 0, false, nil
	}

	body, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
	retryAfter := parseRetryAfter(res.Header.Get("Retry-After"))
	httpErr := &telegramHTTPError{
		status:     res.Status,
		body:       string(body),
		retryAfter: retryAfter,
		transient:  res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500,
	}
	return retryAfter, httpErr.transient, httpErr
}

func truncateTelegramText(text string) string {
	runes := []rune(text)
	if len(runes) > 4000 {
		return string(runes[:4000])
	}
	return text
}

func parseRetryAfter(value string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
