package client

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func TestJobStatusUsesFinalRoutes(t *testing.T) {
	for _, list := range []bool{false, true} {
		t.Run(map[bool]string{false: "detail", true: "list"}[list], func(t *testing.T) {
			transport := transientRoundTripper(func(r *http.Request) (*http.Response, error) {
				path, body := "/api/v1/runs/run%2Fone/jobs/job%2Fone", `{"id":"job/one","status":"running","generation":4,"attempt":2,"state_seq":9,"expansion":"expected"}`
				if list {
					path, body = "/api/v1/runs/run%2Fone/jobs", "["+body+"]"
				}
				if r.Method != "GET" || r.URL.EscapedPath() != path || r.Header.Get("Authorization") != "Bearer private-bearer" {
					t.Fatalf("request=%s %s", r.Method, r.URL.EscapedPath())
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})
			c := NewWithToken("http://example.invalid", &http.Client{Transport: transport}, "private-bearer")
			if list {
				jobs, err := c.ListNodes(t.Context(), "run/one")
				if err != nil || len(jobs) != 1 || jobs[0].NodeID != "job/one" || jobs[0].Status != "running" {
					t.Fatalf("jobs=%+v error=%v", jobs, err)
				}
			} else {
				job, err := c.GetNode(t.Context(), "run/one", "job/one")
				if err != nil || job == nil || job.NodeID != "job/one" || job.Status != "running" {
					t.Fatalf("job=%+v error=%v", job, err)
				}
			}
		})
	}
}

func TestJobStatusUsesCanonicalErrors(t *testing.T) {
	for _, list := range []bool{false, true} {
		for _, body := range []string{`{"error":{"code":"not_found","message":"job missing"}}`, `{"error":{"code":"forbidden","message":"refused"}}`, `{"error":{"error":{"code":"not_found","message":"nested"}}}`, `private malformed error`} {
			t.Run(map[bool]string{false: "detail/", true: "list/"}[list]+body, func(t *testing.T) {
				transport := transientRoundTripper(func(r *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 404, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
				})
				c := New("http://example.invalid", &http.Client{Transport: transport})
				var err error
				if list {
					_, err = c.ListNodes(t.Context(), "run")
				} else {
					_, err = c.GetNode(t.Context(), "run", "job")
				}
				var failure *apierr.Error
				switch {
				case strings.Contains(body, "job missing"):
					if !errors.As(err, &failure) || failure.Code != apierr.NotFound {
						t.Fatalf("error=%v", err)
					}
				case strings.Contains(body, "refused"):
					if !errors.As(err, &failure) || failure.Code != apierr.Forbidden {
						t.Fatalf("error=%v", err)
					}
				default:
					if err == nil || strings.Contains(err.Error(), "private") {
						t.Fatalf("error=%v", err)
					}
				}
			})
		}
	}
}

func TestJobStatusRetainsRetryAdvice(t *testing.T) {
	for _, header := range []string{"Retry-After", "X-Sparkwing-Poll-After"} {
		t.Run(header, func(t *testing.T) {
			transport := transientRoundTripper(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 429, Header: http.Header{header: []string{"11"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"rate_limited","message":"retry later"}}`)), Request: r}, nil
			})
			c := New("http://example.invalid", &http.Client{Transport: transport})
			_, err := c.ListNodes(t.Context(), "run")
			if _, ok := LoadSignal(err); !ok {
				t.Fatalf("error lost retry advice: %v", err)
			}
		})
	}
}
