package handlers

import (
	"net/http"
	"os"
	"strings"
)

// statelessRuntimeEnabled is intentionally provider-neutral even though the
// historical branch/feature flag still contains "cloudrun". New deployments
// may use stateless_azure once main routing is migrated to this helper too.
func statelessRuntimeEnabled() bool {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("INSTAFIX_EXPERIMENT_MODE")))
	switch mode {
	case "stateless", "stateless_azure", "stateless_cloudrun":
		return true
	default:
		return false
	}
}

// rejectStatelessLegacyMedia prevents the Azure experiment from accidentally
// becoming an MP4 relay through a stale legacy URL. Returning 404 rather than a
// redirect is deliberate: direct Instagram CDN URLs must be advertised in the
// embed HTML instead.
func rejectStatelessLegacyMedia(w http.ResponseWriter) bool {
	if !statelessRuntimeEnabled() {
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Instagram7-Experiment", "stateless-azure")
	http.Error(w, "legacy media relay disabled in stateless experiment", http.StatusNotFound)
	return true
}
