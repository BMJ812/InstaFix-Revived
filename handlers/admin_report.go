package handlers

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"instafix/observability"
)

var sendReportPreview = func(ctx context.Context) error {
	return observability.Default.SendReportPreview(ctx)
}

// ReportAdmin sends a real snapshot of the current in-memory production
// counters without resetting them.
func ReportAdmin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")

	configured := strings.TrimSpace(os.Getenv("REPORT_ADMIN_TOKEN"))
	if configured == "" {
		configured = strings.TrimSpace(os.Getenv("COOKIE_ADMIN_TOKEN"))
	}
	provided := strings.TrimSpace(r.Header.Get("X-Admin-Token"))
	if configured == "" || provided == "" ||
		subtle.ConstantTimeCompare([]byte(provided), []byte(configured)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := sendReportPreview(ctx); err != nil {
		slog.Error("Real report preview delivery failed", "err", err)
		http.Error(w, "report delivery failed", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
