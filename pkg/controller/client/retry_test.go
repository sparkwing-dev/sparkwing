package client

import (
	"net/http"
	"testing"
	"time"
)

func TestParseRetryAfterClampsBeforeConverting(t *testing.T) {
	for _, raw := range []string{"9223372036", "92233720368547758", "99999999999999999999999"} {
		resp := &http.Response{Header: http.Header{"Retry-After": []string{raw}}}
		got, ok := parseRetryAfter(resp)
		if !ok || got != maxRetryAfter {
			t.Errorf("Retry-After %s = %s ok=%v, want %s", raw, got, ok, maxRetryAfter)
		}
	}
	resp := &http.Response{Header: http.Header{"Retry-After": []string{"7"}}}
	if got, ok := parseRetryAfter(resp); !ok || got != 7*time.Second {
		t.Errorf("Retry-After 7 = %s ok=%v, want 7s", got, ok)
	}
}
