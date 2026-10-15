package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLegacyEgressTotalsEndpointIsGone(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/internal/egress/totals", strings.NewReader(`{"service":"cache","day":"2026-10-14","month":"2026-10"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	New(nil, nil).Handler().ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("removed egress endpoint: %d %s", response.Code, response.Body.String())
	}
}
