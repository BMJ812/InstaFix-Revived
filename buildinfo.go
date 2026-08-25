package main

import (
	"encoding/json"
	scraper "instafix/handlers/scraper"
	"instafix/observability"
	"net/http"
	"os"
	"runtime"
	"strings"
)

var (
	buildCommit  = "dev"
	buildVersion = "dev"
	buildTime    = "unknown"
)

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	internalMode := strings.ToLower(strings.TrimSpace(os.Getenv("INSTAFIX_EXPERIMENT_MODE")))
	label := strings.TrimSpace(os.Getenv("INSTAFIX_EXPERIMENT_LABEL"))
	if label == "" && internalMode == "stateless_cloudrun" {
		label = "stateless-azure"
	}
	reportedMode := internalMode
	if label == "stateless-azure" {
		// Do not leak the branch's historical compatibility alias into operator
		// status: the actual deployed target/runtime contract is Azure stateless.
		reportedMode = "stateless_azure"
	}
	stateless := reportedMode == "stateless" || reportedMode == "stateless_azure" || internalMode == "stateless_cloudrun"
	payload := map[string]any{
		"ok":               true,
		"commit":           buildCommit,
		"version":          buildVersion,
		"build_time":       buildTime,
		"go":               runtime.Version(),
		"stateless":        stateless,
		"experiment_mode":  reportedMode,
		"experiment_label": label,
	}
	if stateless {
		payload["cache_backend"] = scraper.EphemeralCacheBackend()
		// Keep the experiment observable without requiring paid Log Analytics.
		// Snapshot contains aggregate counters only; it deliberately excludes
		// post IDs, signed CDN URLs, client IPs, cookies, captions and tokens.
		payload["metrics"] = observability.Default.Snapshot()
	}
	_ = json.NewEncoder(w).Encode(payload)
}
