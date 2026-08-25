package main

import (
	"os"
	"strings"
)

// The branch initially introduced its feature gate as "stateless_cloudrun".
// Azure Container Apps is now the selected host, but rewriting the large legacy
// main routing block solely to rename that internal flag would add unnecessary
// risk during the experiment. Accept provider-neutral/Azure names at the process
// boundary and normalize them to the historical internal gate until that routing
// block is extracted behind a proper runtime config object.
func init() {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("INSTAFIX_EXPERIMENT_MODE")))
	switch mode {
	case "stateless", "stateless_azure":
		if strings.TrimSpace(os.Getenv("INSTAFIX_EXPERIMENT_LABEL")) == "" {
			_ = os.Setenv("INSTAFIX_EXPERIMENT_LABEL", "stateless-azure")
		}
		_ = os.Setenv("INSTAFIX_EXPERIMENT_MODE", "stateless_cloudrun")
	case "stateless_cloudrun":
		// Historical branch alias. Keep it working for old local commands while
		// labeling responses with the currently selected Azure experiment target.
		if strings.TrimSpace(os.Getenv("INSTAFIX_EXPERIMENT_LABEL")) == "" {
			_ = os.Setenv("INSTAFIX_EXPERIMENT_LABEL", "stateless-azure")
		}
	}
}
