package originguard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGuard_AllowsLocalCallersRejectsForeignSites(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		method       string
		host         string
		origin       string
		secFetch     string
		secDest      string
		allowRemote  bool
		bindHost     string
		allowOrigins []string
		contentType  string
		want         int
	}{
		{name: "cli has no browser headers", method: http.MethodPost, host: "127.0.0.1:4343", want: http.StatusOK},
		{
			name: "dashboard same origin", method: http.MethodPost, host: "127.0.0.1:4343",
			origin: "http://127.0.0.1:4343", secFetch: "same-origin", want: http.StatusOK,
		},
		{
			name: "next dev on another loopback port without the allow list", method: http.MethodPost, host: "localhost:4343",
			origin: "http://localhost:3100", secFetch: "same-site", want: http.StatusForbidden,
		},
		{
			name: "next dev named on the allow list", method: http.MethodPost, host: "localhost:4343",
			origin: "http://localhost:3100", secFetch: "same-site", allowOrigins: []string{"http://localhost:3100"},
			want: http.StatusOK,
		},
		{
			name: "ipv6 loopback", method: http.MethodGet, host: "[::1]:4343",
			origin: "http://[::1]:4343", want: http.StatusOK,
		},
		{
			name: "foreign site posts", method: http.MethodPost, host: "127.0.0.1:4343",
			origin: "https://evil.example", secFetch: "cross-site", want: http.StatusForbidden,
		},
		{
			name: "foreign site reads", method: http.MethodGet, host: "127.0.0.1:4343",
			origin: "https://evil.example", secFetch: "cross-site", want: http.StatusForbidden,
		},
		{
			name: "opaque origin", method: http.MethodPost, host: "127.0.0.1:4343",
			origin: "null", secFetch: "cross-site", want: http.StatusForbidden,
		},
		{
			name: "cross-site post without origin", method: http.MethodPost, host: "127.0.0.1:4343",
			secFetch: "cross-site", want: http.StatusForbidden,
		},
		{
			name: "cross-site navigation", method: http.MethodGet, host: "127.0.0.1:4343",
			secFetch: "cross-site", secDest: "document", want: http.StatusOK,
		},
		{
			name: "cross-site image load", method: http.MethodGet, host: "127.0.0.1:4343",
			secFetch: "cross-site", secDest: "image", want: http.StatusForbidden,
		},
		{
			name: "cross-site framed page", method: http.MethodGet, host: "127.0.0.1:4343",
			secFetch: "cross-site", secDest: "iframe", want: http.StatusForbidden,
		},
		{
			name: "same-site no-cors fetch", method: http.MethodGet, host: "127.0.0.1:4343",
			secFetch: "same-site", secDest: "empty", want: http.StatusForbidden,
		},
		{
			name: "same-origin subresource", method: http.MethodGet, host: "127.0.0.1:4343",
			secFetch: "same-origin", secDest: "empty", want: http.StatusOK,
		},
		{name: "rebound host", method: http.MethodGet, host: "rebind.example:4343", want: http.StatusForbidden},
		{
			name: "loopback name suffix is not loopback", method: http.MethodGet,
			host: "127.0.0.1.rebind.example:4343", want: http.StatusForbidden,
		},
		{
			name: "remote host with opt-in", method: http.MethodPost, host: "10.0.0.9:4343",
			origin: "http://10.0.0.9:4343", allowRemote: true, bindHost: "10.0.0.9:4343",
			want: http.StatusOK,
		},
		{
			name: "foreign site with opt-in", method: http.MethodPost, host: "10.0.0.9:4343",
			origin: "https://evil.example", allowRemote: true, bindHost: "10.0.0.9:4343",
			want: http.StatusForbidden,
		},
		{
			name: "rebound host is not its own anchor", method: http.MethodPost, host: "rebind.example:4343",
			origin: "http://rebind.example:4343", allowRemote: true, bindHost: "10.0.0.9:4343",
			want: http.StatusForbidden,
		},
		{
			name: "rebound subresource read with opt-in", method: http.MethodGet, host: "rebind.example:4343",
			secFetch: "cross-site", secDest: "image", allowRemote: true, bindHost: "10.0.0.9:4343",
			want: http.StatusForbidden,
		},
		{
			name: "named origin on the allow list", method: http.MethodPost, host: "dash.example",
			origin: "https://dash.example", allowRemote: true, bindHost: "10.0.0.9:4343",
			allowOrigins: []string{"https://dash.example"}, want: http.StatusOK,
		},
		{
			name: "allow list does not cross schemes", method: http.MethodPost, host: "dash.example",
			origin: "http://dash.example", allowRemote: true, bindHost: "10.0.0.9:4343",
			allowOrigins: []string{"https://dash.example"}, want: http.StatusForbidden,
		},
		{
			name: "wildcard bind anchors no origin", method: http.MethodPost, host: "10.0.0.9:4343",
			origin: "http://10.0.0.9:4343", allowRemote: true, want: http.StatusForbidden,
		},
		{
			name: "another loopback port is another program", method: http.MethodPost, host: "127.0.0.1:4343",
			origin: "http://localhost:8080", secFetch: "same-site", want: http.StatusForbidden,
		},
		{
			name: "loopback origin with no port is not this server", method: http.MethodPost, host: "127.0.0.1:4343",
			origin: "http://localhost", secFetch: "same-site", want: http.StatusForbidden,
		},
		{
			name: "browser text/plain write", method: http.MethodPost, host: "127.0.0.1:4343",
			origin: "http://127.0.0.1:4343", secFetch: "same-origin", contentType: "text/plain",
			want: http.StatusUnsupportedMediaType,
		},
		{
			name: "browser form write", method: http.MethodDelete, host: "127.0.0.1:4343",
			origin: "http://127.0.0.1:4343", secFetch: "same-origin", contentType: "application/x-www-form-urlencoded",
			want: http.StatusUnsupportedMediaType,
		},
		{
			name: "browser json write with charset", method: http.MethodPost, host: "127.0.0.1:4343",
			origin: "http://127.0.0.1:4343", secFetch: "same-origin", contentType: "application/json; charset=utf-8",
			want: http.StatusOK,
		},
		{
			name: "loopback proxy forwarding an allow-listed name", method: http.MethodPost, host: "dash.example",
			origin: "https://dash.example", allowOrigins: []string{"https://dash.example"}, want: http.StatusOK,
		},
		{
			name: "rebound host beside an allow list", method: http.MethodGet, host: "rebind.example",
			allowOrigins: []string{"https://dash.example"}, want: http.StatusForbidden,
		},
		{
			name: "cli text write carries no origin", method: http.MethodPost, host: "127.0.0.1:4343",
			contentType: "text/plain", want: http.StatusOK,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			guard := Guard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}), Policy{
				AllowRemote:   tc.allowRemote,
				BindHost:      tc.bindHost,
				LoopbackPorts: []string{"4343"},
				AllowOrigins:  tc.allowOrigins,
			})

			req := httptest.NewRequest(tc.method, "/api/v1/triggers", strings.NewReader("{}"))
			req.Host = tc.host
			contentType := tc.contentType
			if contentType == "" {
				contentType = "application/json"
			}
			req.Header.Set("Content-Type", contentType)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.secFetch != "" {
				req.Header.Set("Sec-Fetch-Site", tc.secFetch)
			}
			if tc.secDest != "" {
				req.Header.Set("Sec-Fetch-Dest", tc.secDest)
			}
			rec := httptest.NewRecorder()
			guard.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}
