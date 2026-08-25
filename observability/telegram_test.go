package observability

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testTelegramClient(server *httptest.Server) *TelegramClient {
	client := NewTelegramClient("test-token", "test-chat")
	client.apiBase = server.URL
	client.client = server.Client()
	client.maxAttempts = 3
	client.retryDelay = 0
	client.maxDelay = 0
	return client
}

func TestTelegramConfigAbsenceLogsClearly(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)

	NewTelegramClient("", "").logConfigState()

	got := logs.String()
	for _, want := range []string{
		"Telegram reports and alerts are disabled",
		"missing_token=true",
		"missing_chat_id=true",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected log to contain %q, got %q", want, got)
		}
	}
}

func TestSendWithRetryRetriesTransientFailures(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/bottest-token/sendMessage" {
			t.Errorf("unexpected path %q", got)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		if got := r.Form.Get("chat_id"); got != "test-chat" {
			t.Errorf("unexpected chat ID %q", got)
		}
		if attempts.Add(1) < 3 {
			http.Error(w, "temporary", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := testTelegramClient(server)
	if err := client.SendWithRetry(context.Background(), "daily report"); err != nil {
		t.Fatalf("SendWithRetry returned error: %v", err)
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("expected 3 attempts, got %d", got)
	}
}

func TestSendWithRetryDoesNotRetryPermanentFailure(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		http.Error(w, "bad chat", http.StatusBadRequest)
	}))
	defer server.Close()

	client := testTelegramClient(server)
	if err := client.SendWithRetry(context.Background(), "daily report"); err == nil {
		t.Fatal("expected permanent Telegram error")
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("expected one attempt for a permanent error, got %d", got)
	}
}

func TestSendHTMLWithRetrySetsParseModeOnlyForHTML(t *testing.T) {
	var parseModes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		parseModes = append(parseModes, r.Form.Get("parse_mode"))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := testTelegramClient(server)
	if err := client.SendWithRetry(context.Background(), "plain alert"); err != nil {
		t.Fatalf("plain send failed: %v", err)
	}
	if err := client.SendHTMLWithRetry(context.Background(), "<b>report</b>"); err != nil {
		t.Fatalf("HTML send failed: %v", err)
	}
	if len(parseModes) != 2 || parseModes[0] != "" || parseModes[1] != "HTML" {
		t.Fatalf("parse modes = %#v, want [\"\", \"HTML\"]", parseModes)
	}
}

func TestAsyncAlertDeliveryIsPreserved(t *testing.T) {
	delivered := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		delivered <- r.Form.Get("text")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := testTelegramClient(server)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go client.Run(ctx)

	client.Send("async alert")
	select {
	case got := <-delivered:
		if got != "async alert" {
			t.Fatalf("unexpected alert text %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for asynchronous alert delivery")
	}
}

func TestSendWithRetryRejectsMissingConfig(t *testing.T) {
	err := NewTelegramClient("", "").SendWithRetry(context.Background(), "report")
	if err != ErrTelegramDisabled {
		t.Fatalf("expected ErrTelegramDisabled, got %v", err)
	}
}
