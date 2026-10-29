package client

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestExchangeGitHubActionsRequiresHTTPSExceptLoopback(t *testing.T) {
	for _, tc := range []struct {
		name       string
		controller string
		requestURL string
		refused    bool
	}{
		{"https", "https://controller.example", "https://actions.example/token", false},
		{"http controller", "http://controller.example", "https://actions.example/token", true},
		{"http token request", "https://controller.example", "http://actions.example/token", true},
		{"both http", "http://controller.example", "http://actions.example/token", true},
		{"loopback IPv4", "http://127.0.0.1:8080", "http://127.0.0.2:9090/token", false},
		{"loopback IPv6", "http://[::1]:8080", "http://[::1]:9090/token", false},
		{"localhost", "http://localhost:8080", "http://localhost:9090/token", false},
		{"private IP", "http://192.168.1.2", "https://actions.example/token", true},
		{"wildcard IP", "http://0.0.0.0", "https://actions.example/token", true},
		{"localhost suffix", "http://localhost.example", "https://actions.example/token", true},
		{"loopback controller remote token", "http://127.0.0.1:8080", "http://actions.example/token", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			options := EngineOptions{URL: tc.controller, HTTP: &http.Client{Transport: engineTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if tc.refused {
					t.Fatal("insecure exchange reached the transport")
				}
				if calls == 1 {
					if req.Method != http.MethodGet || req.URL.Query().Get("audience") != tc.controller || req.Header.Get("Authorization") != "Bearer request-token" {
						t.Fatalf("token request = %s %s, authorization = %q", req.Method, req.URL, req.Header.Get("Authorization"))
					}
					return engineResponse(http.StatusOK, `{"value":"id-token"}`), nil
				}
				if req.Method != http.MethodPost || req.URL.String() != tc.controller+"/api/v1/runners/github/exchange" {
					t.Fatalf("exchange request = %s %s", req.Method, req.URL)
				}
				var input struct {
					Team    string `json:"team"`
					IDToken string `json:"id_token"`
				}
				if err := json.NewDecoder(req.Body).Decode(&input); err != nil || input.Team != "acme" || input.IDToken != "id-token" {
					t.Fatalf("exchange input = %+v, error = %v", input, err)
				}
				return engineResponse(http.StatusOK, `{"token":"runner-token","team":"acme","expires_at":2000000000,"binding":{"kind":"push"}}`), nil
			})}}
			issued, err := ExchangeGitHubActions(t.Context(), options, "acme", tc.requestURL, "request-token")
			if tc.refused {
				if err == nil || !strings.Contains(err.Error(), "must use HTTPS unless loopback") || calls != 0 {
					t.Fatalf("insecure exchange = %+v, %v, calls = %d", issued, err, calls)
				}
				return
			}
			if err != nil || issued.Raw != "runner-token" || issued.Capability.Team != "acme" || calls != 2 {
				t.Fatalf("exchange = %+v, %v, calls = %d", issued, err, calls)
			}
		})
	}
}
