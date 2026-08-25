package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCookieAdminUsesSeparateHelperURLWhenRuntimeFallbackIsDisabled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"accounts":[]}`))
	}))
	defer server.Close()

	t.Setenv("AUTH_HELPER_URL", "")
	t.Setenv("AUTH_HELPER_ADMIN_URL", server.URL)

	accounts, err := loadCookieAccountsFromHelper()
	if err != nil {
		t.Fatalf("loadCookieAccountsFromHelper returned error: %v", err)
	}
	if len(accounts) != 0 {
		t.Fatalf("accounts = %d, want 0", len(accounts))
	}
}
