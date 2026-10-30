package nodehost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func outputPath(job string) string { return "/node/v1/jobs/" + url.PathEscape(job) + "/output" }

func TestOutputReadsANodeOfTheSameRun(t *testing.T) {
	s := start(t)
	service := newService(t)
	service.Output = func(_ context.Context, auth capability.Authorized, job string) (json.RawMessage, error) {
		if auth.Binding.RunID != "run-1" || auth.Binding.NodeID != "deploy" || job != "build/linux" {
			t.Fatalf("output scope=%+v job=%q", auth.Binding, job)
		}
		return json.RawMessage(`{"digest":"sha256:abc"}`), nil
	}
	got := askRoute(t, s, http.MethodGet, outputPath("build/linux"), admitService(t, s, service), "")
	if got.status != http.StatusOK || got.body["output"].(map[string]any)["digest"] != "sha256:abc" {
		t.Fatalf("status %d body %v", got.status, got.body)
	}
}

func TestOutputRefusals(t *testing.T) {
	for _, code := range []apierr.Code{apierr.UnknownJob, apierr.Conflict, apierr.NotFound, apierr.Forbidden, apierr.Unavailable} {
		t.Run(string(code), func(t *testing.T) {
			s := start(t)
			service := newService(t)
			service.Output = func(context.Context, capability.Authorized, string) (json.RawMessage, error) {
				return nil, &apierr.Error{Code: code, Message: "output refused"}
			}
			wantRefusal(t, askRoute(t, s, http.MethodGet, outputPath("build"), admitService(t, s, service), ""), apierr.Status(code), string(code))
		})
	}
	for _, raw := range []json.RawMessage{nil, json.RawMessage(`{"bad"`)} {
		s := start(t)
		service := newService(t)
		service.Output = func(context.Context, capability.Authorized, string) (json.RawMessage, error) { return raw, nil }
		wantRefusal(t, askRoute(t, s, http.MethodGet, outputPath("build"), admitService(t, s, service), ""), http.StatusBadRequest, "protocol_error")
	}
}

func TestPipelineLatestReadsADeclaredReference(t *testing.T) {
	s := start(t)
	service := newService(t)
	service.Latest = func(_ context.Context, auth capability.Authorized, pipeline, job string, age time.Duration, fromAnyRun bool) (ProcessOutput, error) {
		if auth.Binding.RunID != "run-1" || auth.Binding.NodeID != "deploy" || pipeline != "release" || job != "tag" || age != 5*time.Second {
			t.Fatalf("latest scope=%+v %q %q %v", auth.Binding, pipeline, job, age)
		}
		return ProcessOutput{RunID: "run-0", Output: json.RawMessage(`"v1.2.3"`)}, nil
	}
	got := askRoute(t, s, http.MethodGet, "/node/v1/pipelines/release/latest?job=tag&max_age_ms=5000", admitService(t, s, service), "")
	if got.status != http.StatusOK || got.body["run_id"] != "run-0" || got.body["output"] != "v1.2.3" {
		t.Fatalf("status %d body %v", got.status, got.body)
	}
}

func TestPipelineLatestRefusals(t *testing.T) {
	for _, query := range []string{"", "?job=tag&max_age_ms=-1", "?job=tag&max_age_ms=9223372036854775807", "?node=tag"} {
		s := start(t)
		service := newService(t)
		service.Latest = func(context.Context, capability.Authorized, string, string, time.Duration, bool) (ProcessOutput, error) {
			t.Fatal("malformed latest reached callback")
			return ProcessOutput{}, nil
		}
		wantRefusal(t, askRoute(t, s, http.MethodGet, "/node/v1/pipelines/release/latest"+query, admitService(t, s, service), ""), http.StatusBadRequest, "bad_request")
	}
	for _, code := range []apierr.Code{apierr.NotFound, apierr.Forbidden, apierr.Unavailable} {
		s := start(t)
		service := newService(t)
		service.Latest = func(context.Context, capability.Authorized, string, string, time.Duration, bool) (ProcessOutput, error) {
			return ProcessOutput{}, &apierr.Error{Code: code, Message: "latest refused"}
		}
		wantRefusal(t, askRoute(t, s, http.MethodGet, "/node/v1/pipelines/release/latest?job=tag", admitService(t, s, service), ""), apierr.Status(code), string(code))
	}
}
