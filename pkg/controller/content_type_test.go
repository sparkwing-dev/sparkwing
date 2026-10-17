package controller_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func TestRoutes_RejectSimpleRequestContentTypes(t *testing.T) {
	f := newBrowserFixture(t, nil)
	principal, err := f.st.LookupCapability(t.Context(), sha256.Sum256([]byte(f.token)))
	if err != nil {
		t.Fatal(err)
	}
	before := credentialCounts(t, f)
	for _, tc := range []struct{ name, path, body string }{
		{"tokens", "/api/v1/tokens", `{"grants":["runs.read"]}`},
		{"token rotate", "/api/v1/tokens/" + principal.Capability.Prefix + "/rotate", `{"grace_secs":1}`},
		{"secrets", "/api/v1/secrets", `{"name":"pwned","value":"private-value","shared":true}`},
	} {
		for _, contentType := range []string{"", "text/plain;charset=UTF-8", "application/x-www-form-urlencoded", "multipart/form-data; boundary=test"} {
			t.Run(tc.name+"/"+contentType, func(t *testing.T) {
				for _, authenticated := range []bool{false, true} {
					r := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
					r.Header.Set("Content-Type", contentType)
					want := http.StatusUnauthorized
					if authenticated {
						r.Header.Set("Authorization", "Bearer "+f.token)
						want = http.StatusBadRequest
					}
					w := f.serve(r)
					var failure apierr.Envelope
					if w.Code != want || json.Unmarshal(w.Body.Bytes(), &failure) != nil {
						t.Fatalf("HTTP %d: %s, want %d", w.Code, w.Body, want)
					}
					if authenticated && (failure.Error.Code != apierr.BadRequest || !strings.Contains(failure.Error.Message, "application/json")) {
						t.Fatalf("content type refusal=%s", w.Body)
					}
				}
			})
		}
	}
	if after := credentialCounts(t, f); after != before {
		t.Fatalf("refused content type changed credentials: %v -> %v", before, after)
	}
}

func credentialCounts(t *testing.T, f *browserFixture) [3]int {
	t.Helper()
	var out [3]int
	if err := f.st.DB().QueryRowContext(t.Context(), `SELECT (SELECT COUNT(*) FROM capabilities), (SELECT COUNT(*) FROM capabilities WHERE revoked_at IS NOT NULL), (SELECT COUNT(*) FROM secrets)`).Scan(&out[0], &out[1], &out[2]); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRoutes_RejectUnknownJSONFields(t *testing.T) {
	f := newBrowserFixture(t, nil)
	before := credentialCounts(t, f)
	for _, suffix := range []string{`,"extra":1`, `,"extra":{"private":"secret"}`} {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/tokens", strings.NewReader(`{"grants":["runs.read"]`+suffix+`}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+f.token)
		w := f.serve(r)
		var failure apierr.Envelope
		if w.Code != http.StatusBadRequest || json.Unmarshal(w.Body.Bytes(), &failure) != nil || failure.Error.Code != apierr.BadRequest || strings.Contains(w.Body.String(), "secret") {
			t.Fatalf("unknown field refusal: HTTP %d %s", w.Code, w.Body)
		}
	}
	if after := credentialCounts(t, f); after != before {
		t.Fatalf("unknown fields mutated credentials: %v -> %v", before, after)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/tokens", strings.NewReader(`{"grants":["runs.read"]}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+f.token)
	w := f.serve(r)
	if w.Code != http.StatusCreated {
		t.Fatalf("valid JSON: HTTP %d %s", w.Code, w.Body)
	}
}

func TestRoutesUnknownJSONFieldsProperty(t *testing.T) {
	f := newBrowserFixture(t, nil)
	before := credentialCounts(t, f)
	property := func(name, value []byte) bool {
		private := "private-field-" + hex.EncodeToString(value)
		body, err := json.Marshal(map[string]any{"grants": []string{"runs.read"}, "unknown-" + hex.EncodeToString(name): private})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/api/v1/tokens", strings.NewReader(string(body)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+f.token)
		w := f.serve(r)
		var failure apierr.Envelope
		return w.Code == http.StatusBadRequest && json.Unmarshal(w.Body.Bytes(), &failure) == nil && failure.Error.Code == apierr.BadRequest && !strings.Contains(w.Body.String(), private) && !strings.Contains(w.Body.String(), f.token) && credentialCounts(t, f) == before
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
}
