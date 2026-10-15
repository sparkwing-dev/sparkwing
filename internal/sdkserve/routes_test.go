package sdkserve

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

type routeTransport func(*http.Request) (*http.Response, error)

func (f routeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fakeRoutes(t *testing.T, method, path, body, response string) *routeServices {
	t.Helper()
	return &routeServices{
		binding: &nodeproto.ProcessBinding{RunID: "run", JobID: "job", Routes: nodeproto.ProcessRoutes{
			URL: "http://127.0.0.1:4321", Bearer: "delegated-fixture",
		}},
		transport: routeTransport(func(r *http.Request) (*http.Response, error) {
			if r.Method != method || r.URL.RequestURI() != path || r.Header.Get("Authorization") != "Bearer delegated-fixture" {
				t.Errorf("unexpected request: %s %s, authorization=%q", r.Method, r.URL.RequestURI(), r.Header.Get("Authorization"))
			}
			var got, want any
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(body), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("request body = %#v, want %#v", got, want)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(response)), Header: http.Header{}}, nil
		}),
	}
}

func TestHostRoutesUseBoundServices(t *testing.T) {
	t.Run("secret", func(t *testing.T) {
		s := fakeRoutes(t, "GET", "/node/v1/secrets/token%2Fname", `null`, `{"value":"private","masked":true}`)
		value, masked, err := s.Secret(t.Context(), "token/name")
		if err != nil || value != "private" || !masked {
			t.Fatalf("secret = %q, %t, %v", value, masked, err)
		}
	})
	t.Run("output", func(t *testing.T) {
		s := fakeRoutes(t, "GET", "/node/v1/jobs/build%2Fimage/output", `null`, `{"output":{"image":"ready"}}`)
		value, err := s.Output(t.Context(), "build/image")
		if err != nil || string(value) != `{"image":"ready"}` {
			t.Fatalf("output = %s, %v", value, err)
		}
	})
	t.Run("latest", func(t *testing.T) {
		s := fakeRoutes(t, "GET", "/node/v1/pipelines/release%2Fprod/latest?job=build%26image&max_age_ms=60000", `null`, `{"output":42}`)
		value, err := s.Latest(t.Context(), "release/prod", "build&image", time.Minute)
		if err != nil || string(value) != "42" {
			t.Fatalf("latest = %s, %v", value, err)
		}
	})
	t.Run("oidc", func(t *testing.T) {
		s := fakeRoutes(t, "POST", "/node/v1/oidc-token", `{"audience":"deploy"}`, `{"token":"issued"}`)
		value, err := s.OIDC(t.Context(), "deploy")
		if err != nil || value != "issued" {
			t.Fatalf("OIDC = %q, %v", value, err)
		}
	})
	t.Run("cleanup", func(t *testing.T) {
		s := fakeRoutes(t, "POST", "/node/v1/cleanups", `{"argv":["docker","rm","fixture"],"description":"container"}`, `{"id":"cleanup/1"}`)
		id, err := s.RegisterCleanup(t.Context(), []string{"docker", "rm", "fixture"}, "container")
		if err != nil || id != "cleanup/1" {
			t.Fatalf("register cleanup = %q, %v", id, err)
		}
		s = fakeRoutes(t, "DELETE", "/node/v1/cleanups/cleanup%2F1", `null`, `{}`)
		if err := s.ReleaseCleanup(t.Context(), id); err != nil {
			t.Fatal(err)
		}
	})
}

func TestHostRoutesChildSubmissionAndAwait(t *testing.T) {
	for _, outcome := range []string{"success", "memoized"} {
		t.Run(outcome, func(t *testing.T) {
			submit := fakeRoutes(t, "POST", "/node/v1/children",
				`{"pipeline":"build","job":"image","args":{"version":"next"},"repo":"org/repo","branch":"release"}`, `{"run_id":"child/1"}`)
			await := fakeRoutes(t, "GET", "/node/v1/children/child%2F1/await?job=image&wait_ms=60000", `null`, `{"status":"`+outcome+`","output":42}`)
			calls := 0
			s := &routeServices{binding: submit.binding, transport: routeTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return submit.transport.RoundTrip(r)
				}
				return await.transport.RoundTrip(r)
			})}
			got, err := s.RunPipeline(t.Context(), ChildRequest{Pipeline: "build", Job: "image", Args: map[string]string{"version": "next"}, Repo: "org/repo", Branch: "release"})
			if err != nil || string(got) != "42" || calls != 2 {
				t.Fatalf("child = %s, %v, calls = %d", got, err, calls)
			}
		})
	}
}

func TestHostRoutesRefuseInvalidOriginsBeforeSendingBearer(t *testing.T) {
	for _, origin := range []string{"https://127.0.0.1", "http://192.0.2.1", "http://user@127.0.0.1", "http://127.0.0.1/path", "http://127.0.0.1?query=value", "http://127.0.0.1#fragment"} {
		t.Run(origin, func(t *testing.T) {
			s := &routeServices{
				binding: &nodeproto.ProcessBinding{Routes: nodeproto.ProcessRoutes{URL: origin, Bearer: "secret"}},
				transport: routeTransport(func(*http.Request) (*http.Response, error) {
					t.Fatal("sent the delegated bearer to an invalid origin")
					return nil, nil
				}),
			}
			if _, err := s.Output(t.Context(), "job"); err == nil {
				t.Fatal("invalid origin accepted")
			}
		})
	}
}

func TestHostRoutesCancellationReachesTransport(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	s := &routeServices{
		binding: &nodeproto.ProcessBinding{Routes: nodeproto.ProcessRoutes{URL: "http://127.0.0.1"}},
		transport: routeTransport(func(r *http.Request) (*http.Response, error) {
			cancel()
			return nil, r.Context().Err()
		}),
	}
	if _, err := s.Output(ctx, "job"); !errors.Is(err, context.Canceled) {
		t.Fatalf("transport cancellation = %v", err)
	}
}
