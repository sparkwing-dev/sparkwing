package controller_test

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestRoutes_RejectSimpleRequestContentTypes(t *testing.T) {
	base, _, cleanup := newTestServer(t)
	defer cleanup()

	cases := []struct {
		name string
		path string
		body string
	}{
		{name: "login", path: "/api/v1/auth/login", body: `{"username":"a","password":"b"}`},
		{name: "logout", path: "/api/v1/auth/logout", body: `{"session_id":"s"}`},
		{name: "users", path: "/api/v1/users", body: `{"name":"pwn","password":"correct horse"}`},
		{name: "tokens", path: "/api/v1/tokens", body: `{"principal":"p","kind":"user","scopes":["admin"]}`},
		{name: "token rotate", path: "/api/v1/tokens/abcd1234/rotate", body: `{"grace_secs":1}`},
		{name: "secrets", path: "/api/v1/secrets", body: `{"name":"pwned","value":"x"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, got := postAs(t, base+tc.path, "text/plain;charset=UTF-8", tc.body)
			if status != http.StatusBadRequest || !strings.Contains(got, "application/json") {
				t.Fatalf("text/plain status = %d body = %q, want 400 naming the JSON content type", status, got)
			}
		})
	}

	resp := mustGet(t, base+"/api/v1/secrets/pwned")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("secret lookup after the refused write = %d, want 404", resp.StatusCode)
	}

	for _, tc := range cases {
		t.Run(tc.name+" json", func(t *testing.T) {
			_, got := postAs(t, base+tc.path, "application/json", tc.body)
			if strings.Contains(got, "application/json required") {
				t.Fatalf("application/json body was refused: %q", got)
			}
		})
	}
}

func TestRoutes_RejectUnknownJSONFields(t *testing.T) {
	base, _, cleanup := newTestServer(t)
	defer cleanup()

	status, got := postAs(t, base+"/api/v1/tokens", "application/json",
		`{"principal":"p","kind":"user","scopes":["admin"],"extra":1}`)
	if status != http.StatusBadRequest || !strings.Contains(got, "extra") {
		t.Fatalf("unknown field status = %d body = %q, want 400 naming the field", status, got)
	}
}

func postAs(t *testing.T, url, contentType, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}
