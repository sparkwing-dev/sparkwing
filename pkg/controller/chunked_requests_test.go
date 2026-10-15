package controller

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

type unsizedRequestBody struct{ io.Reader }

func TestEngineClaimHTTPChunkedBody(t *testing.T) {
	_, h, raw := engineClaimHTTPFixture(t, []capability.Grant{capability.NodesClaim})
	for _, body := range []string{"", `{"runner_version":"v1","kinds":["node"],"pipelines":["other"]}`, `{"runner_version":"v1","kinds":["node"],"lease_nanos":3600000000000}`} {
		r := httptest.NewRequest("POST", "/api/v1/runners/claim", unsizedRequestBody{strings.NewReader(body)})
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+raw)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("HTTP %d %s", w.Code, w.Body)
		}
	}
	r := httptest.NewRequest("POST", "/api/v1/runners/claim", unsizedRequestBody{strings.NewReader(`{"runner_version":"v1","kinds":["node"],"available_cores":1}`)})
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+raw)
	if r.ContentLength != -1 {
		t.Fatalf("body length = %d", r.ContentLength)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var result controllerwire.ClaimResponse
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Claim == nil || result.Claim.JobID != "job" || result.Claim.Capability == "" {
		t.Fatalf("HTTP %d %s", w.Code, w.Body)
	}
}

func TestCapabilityRotationChunkedBodyPreservesGrace(t *testing.T) {
	for _, tc := range []struct {
		body   string
		want   time.Duration
		status int
	}{
		{`{}`, 24 * time.Hour, 200},
		{`{"grace_secs":3600}`, time.Hour, 200},
		{`{"grace_secs":0}`, 0, 200},
		{"", 0, 400},
	} {
		r := httptest.NewRequest("POST", "/api/v1/tokens/target/rotate", unsizedRequestBody{strings.NewReader(tc.body)})
		r.Header.Set("Content-Type", "application/json")
		if r.ContentLength != -1 {
			t.Fatalf("body length = %d", r.ContentLength)
		}
		grace, _, err := decodeCapabilityRotation(r)
		if tc.status == 400 {
			if err == nil {
				t.Fatal("empty chunked body accepted")
			}
			continue
		}
		if err != nil || grace != tc.want {
			t.Fatalf("grace = %s, error = %v", grace, err)
		}
	}
}
