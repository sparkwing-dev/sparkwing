package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"example.com/paygate/internal/store"
)

func newHandler() *Handler {
	s := store.New()
	s.Put(store.Account{ID: "a1", Email: "a@example.com"})
	return &Handler{Store: s}
}

func TestHealth(t *testing.T) {
	rec := httptest.NewRecorder()
	newHandler().Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestGetAccount(t *testing.T) {
	h := newHandler()

	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/accounts/a1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "a@example.com") {
		t.Errorf("body = %q", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/accounts/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404", rec.Code)
	}
}

func TestInvoiceTotal(t *testing.T) {
	h := newHandler()
	body := strings.NewReader(`[{"Cents":250,"Quantity":4}]`)

	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/invoices/total", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "1000") {
		t.Errorf("body = %q, want cents 1000", rec.Body.String())
	}
}

func TestInvoiceTotalRejectsNegative(t *testing.T) {
	rec := httptest.NewRecorder()
	newHandler().Routes().ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/invoices/total", strings.NewReader(`[{"Cents":-1}]`)))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("code = %d, want 422", rec.Code)
	}
}
