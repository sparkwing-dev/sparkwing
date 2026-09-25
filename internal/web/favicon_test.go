package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestFaviconIsPublicAndMatchesDashboardHost(t *testing.T) {
	bundle := fstest.MapFS{
		"index.html":         &fstest.MapFile{Data: []byte(`<head><link rel="icon" href="/favicon.ico?favicon.1"></head>`)},
		"favicon.ico":        &fstest.MapFile{Data: []byte("blue")},
		"favicon-orange.ico": &fstest.MapFile{Data: []byte("orange")},
		"private.png":        &fstest.MapFile{Data: []byte("private")},
	}
	handler := HandlerFromOptionsWithBundle(HandlerOptions{RequireLogin: true, AuthControllerURL: "http://127.0.0.1:1"}, bundle)
	for _, tc := range []struct {
		host, path, method, wantBody string
		wantStatus                   int
	}{
		{"console.sparkwing.dev", "/favicon.ico", http.MethodGet, "orange", http.StatusOK},
		{"console.sparkwing.dev", "/favicon-orange.ico", http.MethodGet, "orange", http.StatusOK},
		{"console.sparkwing.dev", "/favicon.ico", http.MethodHead, "", http.StatusOK},
		{"localhost:4343", "/favicon.ico", http.MethodGet, "blue", http.StatusOK},
		{"localhost:4343", "/favicon-orange.ico", http.MethodGet, "orange", http.StatusOK},
		{"console.sparkwing.dev", "/private.png", http.MethodGet, "", http.StatusSeeOther},
		{"console.sparkwing.dev", "/favicon.ico/other", http.MethodGet, "", http.StatusSeeOther},
	} {
		t.Run(tc.host+tc.path+tc.method, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.Host = tc.host
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus || (tc.wantStatus == http.StatusOK && rec.Body.String() != tc.wantBody) {
				t.Errorf("status/body = %d/%q, want %d/%q", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
		})
	}
}

func TestDashboardFaviconLinkMatchesHost(t *testing.T) {
	bundle := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte(`<head><link rel="icon" href="/favicon.ico?favicon.1"></head>`)},
	}
	handler := HandlerFromOptionsWithBundle(HandlerOptions{}, bundle)
	for _, tc := range []struct{ host, want string }{
		{"console.sparkwing.dev", "/favicon-orange.ico?favicon.1"},
		{"localhost:4343", "/favicon.ico?favicon.1"},
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = tc.host
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("HTML = %q, want favicon %q", rec.Body.String(), tc.want)
		}
	}
}
