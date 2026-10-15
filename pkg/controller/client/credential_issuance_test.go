package client

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCredentialIssuanceRequestsAndLostResponse(t *testing.T) {
	for _, resource := range []string{"member", "machine", "service"} {
		for _, status := range []int{201, 503} {
			calls := 0
			transport := transientRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				path := "/api/v1/tokens"
				switch resource {
				case "member":
					if len(body) != 1 || string(body["grants"]) != `["runs.read"]` {
						t.Fatalf("body=%s", body)
					}
				case "machine":
					path = "/api/v1/runners"
					if len(body) != 2 || string(body["name"]) != `"builder"` || string(body["repos"]) != `["github.com/acme/*"]` {
						t.Fatalf("body=%s", body)
					}
				case "service":
					path = "/api/v1/operator/tokens"
					if len(body) != 2 || string(body["name"]) != `"launcher"` || string(body["grants"]) != `["claims.launch"]` {
						t.Fatalf("body=%s", body)
					}
				}
				if r.Method != "POST" || r.URL.Path != path || r.Header.Get("Authorization") != "Bearer private" {
					t.Fatalf("request=%+v", r)
				}
				response := `{"capability":"one-time-bearer","prefix":"prefix","team":"team","grants":["runs.read"],"expires_at":"2100-01-01T00:00:00Z"}`
				if status == 503 {
					response = `{"error":{"code":"unavailable","message":"issuance unavailable"}}`
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": []string{"0"}}, Body: io.NopCloser(strings.NewReader(response)), Request: r}, nil
			})
			c := NewWithToken("http://example.invalid", &http.Client{Transport: transport}, "private")
			var got *CreatedToken
			var err error
			switch resource {
			case "member":
				got, err = c.CreateToken(t.Context(), CreateTokenRequest{Grants: []string{"runs.read"}})
			case "machine":
				got, err = c.EnrollRunner(t.Context(), EnrollRunnerRequest{Name: "builder", Repos: []string{"github.com/acme/*"}})
			case "service":
				got, err = c.CreateServiceToken(t.Context(), CreateServiceTokenRequest{Name: "launcher", Grants: []string{"claims.launch"}})
			}
			if calls != 1 || (err != nil) != (status == 503) {
				t.Fatalf("%s calls=%d err=%v", resource, calls, err)
			}
			if status == 503 {
				if got != nil {
					t.Fatalf("failed issuance returned %+v", got)
				}
			} else if got == nil || got.Capability != "one-time-bearer" || got.Prefix != "prefix" || got.ExpiresAt == nil || got.ExpiresAt.Year() != 2100 {
				t.Fatalf("response=%+v", got)
			}
		}
	}
}
