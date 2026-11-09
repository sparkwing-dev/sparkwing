package releaseasset

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

type downloadTransport func(*http.Request) (*http.Response, error)

func (f downloadTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDownloadReleaseAPIFallback(t *testing.T) {
	for _, mode := range []string{"504", "timeout", "body-timeout", "404", "403", "200", "missing", "api-failure", "metadata-oversize", "oversize", "custom"} {
		t.Run(mode, func(t *testing.T) {
			var apiCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/org/repo/releases/download/v0.1.0/SHA256SUMS":
					switch mode {
					case "404":
						w.WriteHeader(http.StatusNotFound)
					case "403":
						w.WriteHeader(http.StatusForbidden)
					case "200":
						_, _ = io.WriteString(w, "signed bytes")
					default:
						w.WriteHeader(http.StatusGatewayTimeout)
					}
				case "/repos/org/repo/releases/tags/v0.1.0":
					apiCalls.Add(1)
					if mode == "metadata-oversize" {
						_, _ = io.WriteString(w, strings.Repeat(" ", (4<<20)+1))
						return
					}
					if mode == "api-failure" {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					name := "SHA256SUMS"
					if mode == "missing" {
						name = "other"
					}
					_, _ = fmt.Fprintf(w, `{"assets":[{"id":42,"name":%q,"url":"https://evil.invalid/ignored"}]}`, name)
				case "/repos/org/repo/releases/assets/42":
					apiCalls.Add(1)
					if r.Header.Get("Accept") != "application/octet-stream" {
						t.Error("missing binary Accept header")
					}
					_, _ = io.WriteString(w, "signed bytes")
				default:
					t.Errorf("unexpected request %s", r.URL)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			endpoint, _ := url.Parse(server.URL)
			client := &http.Client{Transport: downloadTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == "github.com" && mode == "timeout" {
					return nil, os.ErrDeadlineExceeded
				}
				if r.URL.Host == "github.com" && mode == "body-timeout" {
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(timeoutReader{}), Header: make(http.Header), Request: r}, nil
				}
				r = r.Clone(r.Context())
				r.URL.Scheme, r.URL.Host = endpoint.Scheme, endpoint.Host
				return http.DefaultTransport.RoundTrip(r)
			})}
			address := "https://github.com/org/repo/releases/download/v0.1.0/SHA256SUMS"
			if mode == "custom" {
				address = strings.Replace(address, "github.com", "custom.invalid", 1)
			}
			limit := int64(100)
			if mode == "oversize" {
				limit = 3
			}
			body, err := Download(context.Background(), client, address, limit)
			success := mode == "504" || mode == "timeout" || mode == "body-timeout" || mode == "200"
			if success && (err != nil || string(body) != "signed bytes") {
				t.Fatalf("body=%q err=%v", body, err)
			}
			if !success && err == nil {
				t.Fatal("expected failure")
			}
			if mode == "metadata-oversize" && (!strings.Contains(err.Error(), "4194304-byte limit") || !strings.Contains(err.Error(), "retry the release download") || !strings.Contains(err.Error(), "console Help menu")) {
				t.Fatalf("missing metadata remedy: %v", err)
			}
			expected := 2
			switch mode {
			case "403", "404", "200", "custom":
				expected = 0
			case "missing", "api-failure", "metadata-oversize":
				expected = 1
			}
			if apiCalls.Load() != int32(expected) {
				t.Fatalf("API requests=%d, want %d", apiCalls.Load(), expected)
			}
		})
	}
}

type timeoutReader struct{}

func (timeoutReader) Read([]byte) (int, error) { return 0, os.ErrDeadlineExceeded }
