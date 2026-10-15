package nodehost_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
)

func TestChildStartsAndAwaitsItsOutput(t *testing.T) {
	s := start(t)
	service := newService(t)
	service.StartChild = func(_ context.Context, auth capability.Authorized, req nodehost.ChildRequest) (string, error) {
		if auth.Binding.RunID != "run-1" || auth.Binding.NodeID != "deploy" || req.Pipeline != "build" || req.Args["tag"] != "1" {
			t.Fatalf("start binding=%+v request=%+v", auth.Binding, req)
		}
		return "child-1", nil
	}
	service.AwaitChild = func(_ context.Context, auth capability.Authorized, run, job string, wait time.Duration) (nodehost.ProcessChildResult, error) {
		if auth.Binding.RunID != "run-1" || auth.Binding.NodeID != "deploy" || run != "child-1" || job != "build" || wait != 0 {
			t.Fatalf("await binding=%+v %q %q %v", auth.Binding, run, job, wait)
		}
		return nodehost.ProcessChildResult{RunID: run, Status: "success", Output: json.RawMessage(`{"tag":"v1"}`)}, nil
	}
	bearer := admitService(t, s, service)
	got := ask(t, s, http.MethodPost, "/node/v1/children", bearer, `{"pipeline":"build","args":{"tag":"1"}}`)
	if got.status != http.StatusCreated || got.body["run_id"] != "child-1" {
		t.Fatalf("start=%+v", got)
	}
	got = ask(t, s, http.MethodGet, "/node/v1/children/child-1/await?job=build&wait_ms=0", bearer, "")
	if got.status != http.StatusOK || got.body["status"] != "success" || got.body["output"].(map[string]any)["tag"] != "v1" {
		t.Fatalf("await=%+v", got)
	}
}

func TestChildStartRefusals(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `{"pipeline":"build","args":null}`, `{"pipeline":"build","pipeline":"other"}`, `{"pipeline":"build"} {}`} {
		s := start(t)
		service := newService(t)
		service.StartChild = func(context.Context, capability.Authorized, nodehost.ChildRequest) (string, error) {
			t.Fatal("malformed child reached admission")
			return "", nil
		}
		wantRefusal(t, ask(t, s, http.MethodPost, "/node/v1/children", admitService(t, s, service), body), http.StatusBadRequest, "bad_request")
	}
	for _, code := range []apierr.Code{apierr.PlanInvalid, apierr.Conflict, apierr.Unavailable} {
		s := start(t)
		service := newService(t)
		service.StartChild = func(context.Context, capability.Authorized, nodehost.ChildRequest) (string, error) {
			return "", &apierr.Error{Code: code, Message: "admission refused"}
		}
		wantRefusal(t, ask(t, s, http.MethodPost, "/node/v1/children", admitService(t, s, service), `{"pipeline":"build"}`), apierr.Status(code), string(code))
	}
}

func TestAwaitRefusals(t *testing.T) {
	for _, wait := range []string{"-1", "later", "60001"} {
		s := start(t)
		service := newService(t)
		service.AwaitChild = func(context.Context, capability.Authorized, string, string, time.Duration) (nodehost.ProcessChildResult, error) {
			t.Fatal("invalid wait reached child adapter")
			return nodehost.ProcessChildResult{}, nil
		}
		wantRefusal(t, ask(t, s, http.MethodGet, "/node/v1/children/child/await?wait_ms="+wait, admitService(t, s, service), ""), http.StatusBadRequest, "bad_request")
	}
	for _, code := range []apierr.Code{apierr.NotFound, apierr.UnknownJob, apierr.Forbidden, apierr.Unavailable} {
		s := start(t)
		service := newService(t)
		service.AwaitChild = func(context.Context, capability.Authorized, string, string, time.Duration) (nodehost.ProcessChildResult, error) {
			return nodehost.ProcessChildResult{}, &apierr.Error{Code: code, Message: "child refused"}
		}
		wantRefusal(t, ask(t, s, http.MethodGet, "/node/v1/children/child/await", admitService(t, s, service), ""), apierr.Status(code), string(code))
	}
}
