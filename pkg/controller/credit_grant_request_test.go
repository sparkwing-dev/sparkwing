package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreditGrantRequestRefusesCallerSelectedCheckout(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/credits/grants", strings.NewReader(`{"kind":"paid","amount_micro":1000000,"reference":"payment","checkout":"session"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	(&Server{}).handleCreditsGrant(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("caller-selected checkout status=%d, body=%s", response.Code, response.Body)
	}
}
