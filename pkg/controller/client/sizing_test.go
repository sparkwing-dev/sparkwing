package client

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestSizingUsesFinalRoutes(t *testing.T) {
	for _, leaf := range []string{"", "/pin", "/observations", "/contention", "/waits"} {
		t.Run(leaf, func(t *testing.T) {
			calls := 0
			transport := transientRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.EscapedPath() != "/api/v1/pipelines/ci%2Fbuild/sizing"+leaf {
					t.Fatalf("path=%s", r.URL.EscapedPath())
				}
				job := "job"
				if leaf == "/contention" || leaf == "/waits" {
					job = ""
				}
				if r.URL.Query().Get("job") != job || r.URL.Query().Has("node") {
					t.Fatalf("query=%s", r.URL.RawQuery)
				}
				status, body := 204, ""
				if leaf == "" {
					status, body = 200, `{"pipeline":"ci/build","node_id":"job","pinned_cores":1.5}`
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})
			c := New("http://example.invalid", &http.Client{Transport: transport})
			var err error
			switch leaf {
			case "":
				var profile *store.PipelineProfile
				profile, err = c.GetPipelineProfile(t.Context(), "ci/build", "job")
				if err == nil && (profile == nil || profile.PinnedCores != 1.5) {
					t.Fatalf("profile=%+v", profile)
				}
			case "/pin":
				err = c.SetPipelinePin(t.Context(), "ci/build", "job", 1.5, 2048)
			case "/observations":
				err = c.RecordProfileObservation(t.Context(), "ci/build", "job", store.ProfileObservation{PeakCores: 1.5, CPUMeasured: true})
			case "/contention":
				err = c.RecordContention(t.Context(), "ci/build")
			case "/waits":
				err = c.RecordWaitObservation(t.Context(), "ci/build", time.Second)
			}
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("calls=%d", calls)
			}
		})
	}
}

func TestSizingMissingMeasurementUsesCanonicalError(t *testing.T) {
	for _, body := range []string{`{"error":{"code":"not_found","message":"sizing record not found"}}`, `{"error":{"code":"forbidden","message":"not readable"}}`, `not JSON`, `{"error":{"error":{"code":"not_found","message":"nested"}}}`} {
		t.Run(body, func(t *testing.T) {
			transport := transientRoundTripper(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 404, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})
			c := New("http://example.invalid", &http.Client{Transport: transport})
			profile, err := c.GetPipelineProfile(t.Context(), "build", "job")
			if strings.Contains(body, "sizing record not found") {
				if profile != nil || err != nil {
					t.Fatalf("profile=%v error=%v", profile, err)
				}
			} else if strings.Contains(body, "not readable") {
				var failure *apierr.Error
				if !errors.As(err, &failure) || failure.Code != apierr.Forbidden {
					t.Fatalf("error=%v", err)
				}
			} else if err == nil {
				t.Fatal("malformed error treated as an absent measurement")
			}
		})
	}
}
