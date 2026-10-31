package sdkserve

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func FuzzHostRouteIdentifiers(f *testing.F) {
	for _, id := range []string{"job", ".", "..", "/", "../other", "a/b", "a//b", "%2e%2e", "a?b#c", "a\\b", "作業", "\x00"} {
		f.Add(id)
	}
	f.Fuzz(func(t *testing.T, id string) {
		if id == "" || !utf8.ValidString(id) {
			return
		}
		for _, test := range []struct {
			pattern, reply string
			call           func(*routeServices) error
		}{
			{"GET /node/v1/secrets/{id}", `{"value":"","masked":false}`, func(s *routeServices) error {
				_, _, err := s.Secret(t.Context(), id)
				return err
			}},
			{"GET /node/v1/jobs/{id}/output", `{"output":null}`, func(s *routeServices) error {
				_, err := s.Output(t.Context(), id)
				return err
			}},
			{"GET /node/v1/pipelines/{id}/latest", `{"output":null}`, func(s *routeServices) error {
				_, err := s.Latest(t.Context(), id, "job", time.Minute, false)
				return err
			}},
			{"GET /node/v1/children/{id}/await", `{"status":"success","output":null}`, func(s *routeServices) error {
				_, err := s.RunPipeline(t.Context(), ChildRequest{Pipeline: "pipeline", Job: "job", Args: map[string]string{}})
				return err
			}},
			{"DELETE /node/v1/cleanups/{id}", `{}`, func(s *routeServices) error {
				return s.ReleaseCleanup(t.Context(), id)
			}},
		} {
			method, pattern, _ := strings.Cut(test.pattern, " ")
			prefix, suffix, _ := strings.Cut(pattern, "{id}")
			calls := 0
			s := &routeServices{
				binding: &nodeproto.ProcessBinding{Routes: nodeproto.ProcessRoutes{URL: "http://127.0.0.1", Bearer: "delegated-fixture"}},
				transport: routeTransport(func(r *http.Request) (*http.Response, error) {
					w := httptest.NewRecorder()
					if r.Method == http.MethodPost && r.URL.Path == "/node/v1/children" {
						_ = json.NewEncoder(w).Encode(map[string]string{"run_id": id})
						return w.Result(), nil
					}
					calls++
					escaped := r.URL.EscapedPath()
					if r.Method != method || !strings.HasPrefix(escaped, prefix) || !strings.HasSuffix(escaped, suffix) || r.URL.Fragment != "" {
						t.Fatalf("%s with %q reached %s %s", test.pattern, id, r.Method, r.URL)
					}
					segment := strings.TrimSuffix(strings.TrimPrefix(escaped, prefix), suffix)
					if segment == "." || segment == ".." || strings.Contains(segment, "/") {
						t.Fatalf("%s with %q is not an opaque segment: %q", test.pattern, id, segment)
					}
					if got, err := url.PathUnescape(segment); err != nil || got != id {
						t.Fatalf("%s identifier = %q, %v, want %q", test.pattern, got, err, id)
					}
					if got := r.Header.Get("Authorization"); got != "Bearer delegated-fixture" {
						t.Errorf("route authorization = %q", got)
					}
					_, _ = w.Write([]byte(test.reply))
					return w.Result(), nil
				}),
			}
			if err := test.call(s); err != nil || calls != 1 {
				t.Fatalf("%s with %q: calls = %d, error = %v", test.pattern, id, calls, err)
			}
		}
	})
}
