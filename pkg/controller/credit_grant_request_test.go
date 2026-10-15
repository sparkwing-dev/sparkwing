package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func FuzzLegacyPaidCreditRouteIsGone(f *testing.F) {
	f.Add("", "a", "payment", int64(1000000))
	f.Add("Bearer billing", "default", "session", int64(-1))
	f.Fuzz(func(t *testing.T, auth, team, reference string, amount int64) {
		body, err := json.Marshal(map[string]any{"kind": "paid", "team": team, "reference": reference, "checkout": reference, "amount_micro": amount})
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/api/v1/credits/grants", strings.NewReader(string(body)))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", auth)
		response := httptest.NewRecorder()
		New(nil, nil).Handler().ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("retired paid grant route: %d %s", response.Code, response.Body)
		}
	})
}
