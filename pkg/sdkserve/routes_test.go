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

	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
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
		for _, output := range []string{"42", "null", ""} {
			t.Run(outcome+"/output="+output, func(t *testing.T) {
				submit := fakeRoutes(t, "POST", "/node/v1/children",
					`{"pipeline":"build","job":"image","args":{"version":"next"},"repo":"org/repo","branch":"release"}`, `{"run_id":"child/1"}`)
				response := `{"status":"` + outcome + `"`
				if output != "" {
					response += `,"output":` + output
				}
				await := fakeRoutes(t, "GET", "/node/v1/children/child%2F1/await?job=image&wait_ms=60000", `null`, response+`}`)
				calls := 0
				s := &routeServices{binding: submit.binding, transport: routeTransport(func(r *http.Request) (*http.Response, error) {
					calls++
					if calls == 1 {
						return submit.transport.RoundTrip(r)
					}
					return await.transport.RoundTrip(r)
				})}
				got, err := s.RunPipeline(t.Context(), ChildRequest{Pipeline: "build", Job: "image", Args: map[string]string{"version": "next"}, Repo: "org/repo", Branch: "release"})
				if (err == nil) != (output != "") || string(got) != output || calls != 2 {
					t.Fatalf("child = %s, %v, calls = %d", got, err, calls)
				}
			})
		}
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

func TestHostRoutesRejectIncompleteResponses(t *testing.T) {
	for _, test := range []struct {
		name, method, path, body string
		invalid                  []string
		call                     func(*routeServices) error
	}{
		{"secret", "GET", "/node/v1/secrets/TOKEN", `null`, []string{
			`null`, `{}`, `{"value":"private"}`, `{"value":"private","masked":null}`, `{"value":null,"masked":true}`,
		}, func(s *routeServices) error { _, _, err := s.Secret(t.Context(), "TOKEN"); return err }},
		{
			"output", "GET", "/node/v1/jobs/job/output", `null`,
			[]string{`null`, `{}`},
			func(s *routeServices) error { _, err := s.Output(t.Context(), "job"); return err },
		},
		{
			"latest", "GET", "/node/v1/pipelines/p/latest?job=job&max_age_ms=0", `null`,
			[]string{`null`, `{}`},
			func(s *routeServices) error { _, err := s.Latest(t.Context(), "p", "job", 0); return err },
		},
		{
			"oidc", "POST", "/node/v1/oidc-token", `{"audience":"deploy"}`,
			[]string{`null`, `{}`, `{"token":null}`, `{"token":""}`},
			func(s *routeServices) error { _, err := s.OIDC(t.Context(), "deploy"); return err },
		},
		{
			"cleanup", "POST", "/node/v1/cleanups", `{"argv":["clean"],"description":"fixture"}`,
			[]string{`null`, `{}`, `{"id":null}`, `{"id":""}`},
			func(s *routeServices) error {
				_, err := s.RegisterCleanup(t.Context(), []string{"clean"}, "fixture")
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, response := range test.invalid {
				t.Run(response, func(t *testing.T) {
					service := fakeRoutes(t, test.method, test.path, test.body, response)
					if err := test.call(service); err == nil {
						t.Fatal("incomplete host response reported success")
					}
				})
			}
		})
	}
}

func TestHostRoutesPreserveExplicitEmptyValues(t *testing.T) {
	service := fakeRoutes(t, "GET", "/node/v1/secrets/EMPTY", `null`, `{"value":"","masked":false,"future":true}`)
	if value, masked, err := service.Secret(t.Context(), "EMPTY"); err != nil || value != "" || masked {
		t.Fatalf("empty variable = %q, %t, %v", value, masked, err)
	}
	service = fakeRoutes(t, "GET", "/node/v1/jobs/job/output", `null`, `{"output":null,"future":true}`)
	if output, err := service.Output(t.Context(), "job"); err != nil || string(output) != "null" {
		t.Fatalf("explicit null output = %s, %v", output, err)
	}
	service = fakeRoutes(t, "GET", "/node/v1/pipelines/p/latest?job=job&max_age_ms=0", `null`, `{"output":null}`)
	if output, err := service.Latest(t.Context(), "p", "job", 0); err != nil || string(output) != "null" {
		t.Fatalf("explicit null latest output = %s, %v", output, err)
	}
}

func TestHostRoutesEncodeNilCollectionsAsEmpty(t *testing.T) {
	submit := fakeRoutes(t, "POST", "/node/v1/children", `{"pipeline":"weather-report","job":"stamp","args":{}}`, `{"run_id":"child"}`)
	await := fakeRoutes(t, "GET", "/node/v1/children/child/await?job=stamp&wait_ms=60000", `null`, `{"status":"success","output":{}}`)
	transport := submit.transport
	calls := 0
	submit.transport = routeTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return transport.RoundTrip(r)
		}
		return await.transport.RoundTrip(r)
	})
	request := ChildRequest{Pipeline: "weather-report", Job: "stamp"}
	if _, err := submit.RunPipeline(t.Context(), request); err != nil || calls != 2 || request.Args != nil {
		t.Fatalf("child request = %+v, calls = %d, error = %v", request, calls, err)
	}
	cleanup := fakeRoutes(t, "POST", "/node/v1/cleanups", `{"argv":[],"description":""}`, `{"id":"cleanup"}`)
	if _, err := cleanup.RegisterCleanup(t.Context(), nil, ""); err != nil {
		t.Fatal(err)
	}
}

func TestMaskUsesAcknowledgedBoundRoute(t *testing.T) {
	s := fakeRoutes(t, "POST", "/node/v1/masks", `{"value":"sensitive"}`, `{}`)
	if err := s.Mask(t.Context(), "sensitive"); err != nil {
		t.Fatal(err)
	}
	s.transport = routeTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusConflict, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"secret_in_identifier","message":"identifier collision","details":{"field":"jobs[0].id"}}}`)), Header: http.Header{}}, nil
	})
	if err := s.Mask(t.Context(), "sensitive"); err == nil || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("mask refusal = %v", err)
	}
	s.binding = nil
	if err := s.Mask(t.Context(), "sensitive"); err == nil {
		t.Fatal("unbound mask accepted")
	}
}

func TestRecordsUseAcknowledgedBoundRoutes(t *testing.T) {
	for _, test := range []struct {
		name, path, body string
		frame            nodeproto.FrameLog
	}{
		{"annotation", "/node/v1/annotations", `{"level":"warn","message":"message","file":"main.go","line":3,"end_line":5,"column":2,"step":"s"}`, nodeproto.FrameLog{Event: "annotation", Level: "warn", Message: "message", Step: "s", Attrs: json.RawMessage(`{"file":"main.go","line":3,"end_line":5,"column":2}`)}},
		{"summary", "/node/v1/summary", `{"markdown":"# Markdown","step":"s"}`, nodeproto.FrameLog{Event: "summary", Message: "# Markdown", Step: "s"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := fakeRoutes(t, "POST", test.path, test.body, `{}`)
			s.ctx = t.Context()
			s.Emit(test.frame)
			s.transport = routeTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"unavailable","message":"write failed"}}`)), Header: http.Header{}}, nil
			})
			defer func() {
				if recover() == nil {
					t.Fatal("record write failure was ignored")
				}
			}()
			s.Emit(test.frame)
		})
	}
	for _, ack := range []string{`null`, `[]`, `"ok"`, ``} {
		t.Run("mask-ack="+ack, func(t *testing.T) {
			s := fakeRoutes(t, "POST", "/node/v1/masks", `{"value":"secret"}`, ack)
			if err := s.Mask(t.Context(), "secret"); err == nil {
				t.Fatal("invalid acknowledgement accepted")
			}
		})
	}
}
