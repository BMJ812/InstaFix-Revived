package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReportAdminRequiresToken(t *testing.T) {
	t.Setenv("REPORT_ADMIN_TOKEN", "secret")
	called := false
	original := sendReportPreview
	sendReportPreview = func(context.Context) error {
		called = true
		return nil
	}
	t.Cleanup(func() { sendReportPreview = original })

	request := httptest.NewRequest(http.MethodPost, "/admin/report/test", nil)
	response := httptest.NewRecorder()
	ReportAdmin(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
	if called {
		t.Fatal("unauthorized request sent a report")
	}
}

func TestReportAdminSendsSnapshot(t *testing.T) {
	t.Setenv("REPORT_ADMIN_TOKEN", "secret")
	called := false
	original := sendReportPreview
	sendReportPreview = func(context.Context) error {
		called = true
		return nil
	}
	t.Cleanup(func() { sendReportPreview = original })

	request := httptest.NewRequest(http.MethodPost, "/admin/report/test", nil)
	request.Header.Set("X-Admin-Token", "secret")
	response := httptest.NewRecorder()
	ReportAdmin(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
	if !called {
		t.Fatal("authorized request did not send a report")
	}
}

func TestReportAdminReportsDeliveryFailure(t *testing.T) {
	t.Setenv("REPORT_ADMIN_TOKEN", "secret")
	original := sendReportPreview
	sendReportPreview = func(context.Context) error {
		return errors.New("Telegram unavailable")
	}
	t.Cleanup(func() { sendReportPreview = original })

	request := httptest.NewRequest(http.MethodPost, "/admin/report/test", nil)
	request.Header.Set("X-Admin-Token", "secret")
	response := httptest.NewRecorder()
	ReportAdmin(response, request)

	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadGateway)
	}
}
