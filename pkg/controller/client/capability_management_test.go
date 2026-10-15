package client

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestCapabilityManagementReadsMetadata(t *testing.T) {
	for _, lookup := range []bool{false, true} {
		transport := transientRoundTripper(func(r *http.Request) (*http.Response, error) {
			path := "/api/v1/tokens"
			body := `{"prefix":"prefix","team":"team","subject":{"kind":"machine","id":"runner"},"audience":"controller","grants":["nodes.claim"],"issued_at":"2026-10-14T12:00:00Z","last_used_at":"2026-10-14T13:00:00Z","metered":true}`
			if lookup {
				path += "/prefix"
			} else {
				body = `{"tokens":[` + body + `]}`
				if r.URL.Query().Get("kind") != "machine" || r.URL.Query().Get("include_revoked") != "1" {
					t.Fatalf("query=%s", r.URL.RawQuery)
				}
			}
			if r.Method != "GET" || r.URL.Path != path || r.Header.Get("Authorization") != "Bearer private" {
				t.Fatalf("request=%+v", r)
			}
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		})
		c := NewWithToken("http://example.invalid", &http.Client{Transport: transport}, "private")
		var got TokenInfo
		if lookup {
			raw, err := c.LookupToken(t.Context(), "prefix")
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
		} else {
			rows, err := c.ListTokens(t.Context(), "machine", true)
			if err != nil || len(rows) != 1 {
				t.Fatalf("rows=%+v err=%v", rows, err)
			}
			got = rows[0]
		}
		if got.Subject.Kind != "machine" || got.Subject.ID != "runner" || len(got.Grants) != 1 || got.Grants[0] != "nodes.claim" || got.IssuedAt.Year() != 2026 || got.LastUsedAt == nil || got.LastUsedAt.Hour() != 13 || !got.Metered {
			t.Fatalf("metadata=%+v", got)
		}
	}
}

func TestCapabilityManagementRotationIsNotReplayed(t *testing.T) {
	for _, status := range []int{200, 503} {
		calls := 0
		expires := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
		transport := transientRoundTripper(func(r *http.Request) (*http.Response, error) {
			calls++
			var body struct {
				Grace   int64     `json:"grace_secs"`
				Expires time.Time `json:"expires_at"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Grace != 0 || body.Expires != expires || r.URL.Path != "/api/v1/tokens/prefix/rotate" || r.Method != "POST" {
				t.Fatalf("body=%+v request=%+v", body, r)
			}
			response := `{"capability":"new-private","new":{"prefix":"replacement","subject":{"kind":"account","id":"member"},"grants":[]},"old_prefix":"prefix","old_revoked_at":"2026-10-14T12:00:00Z"}`
			if status != 200 {
				response = `{"error":{"code":"unavailable","message":"rotation unavailable"}}`
			}
			return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": {"0"}}, Body: io.NopCloser(strings.NewReader(response)), Request: r}, nil
		})
		c := New("http://example.invalid", &http.Client{Transport: transport})
		result, err := c.RotateToken(t.Context(), "prefix", 0, &expires)
		if status == 200 {
			if err != nil || result == nil || result.Capability != "new-private" || result.New.Subject.ID != "member" || result.OldPrefix != "prefix" || result.OldRevokedAt.Year() != 2026 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		} else if err == nil || result != nil {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		if calls != 1 {
			t.Fatalf("rotation replayed %d times", calls)
		}
	}
}
