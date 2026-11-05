package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/apierrhttp"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func legacyCreateRunType() reflect.Type {
	// hack: controllers through v0.68.6 decode the same request fields without the agent label.
	strip := func(t reflect.Type, replace map[string]reflect.Type) reflect.Type {
		var fields []reflect.StructField
		for i := range t.NumField() {
			field := t.Field(i)
			if field.Name == "Agent" {
				continue
			}
			if replacement, ok := replace[field.Name]; ok {
				field.Type = replacement
			}
			fields = append(fields, field)
		}
		return reflect.StructOf(fields)
	}
	request := strip(reflect.TypeFor[runspec.Request](), nil)
	return strip(reflect.TypeFor[controllerwire.CreateRunRequest](), map[string]reflect.Type{"Request": request})
}

func legacyController(t *testing.T, next http.Handler, posts *atomic.Int32) *httptest.Server {
	t.Helper()
	legacy := legacyCreateRunType()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/runs" {
			posts.Add(1)
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			decoder := json.NewDecoder(bytes.NewReader(body))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(reflect.New(legacy).Interface()); err != nil {
				_ = apierrhttp.Write(w, &apierr.Error{Code: apierr.BadRequest, Message: "invalid engine request: body has a field this endpoint does not accept"})
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		next.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return server
}

func engineClientFor(t *testing.T, url, token string) controllerwire.Controller {
	t.Helper()
	engine, err := client.NewEngineClient(client.EngineOptions{URL: url, Bearer: func(_ context.Context) (string, error) { return token, nil }})
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func TestNewClientDropsTheAgentLabelForAnOlderController(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{})
	var posts atomic.Int32
	server := legacyController(t, f.h, &posts)
	engine := engineClientFor(t, server.URL, f.token)

	body := computeIntake("build")
	body.Request.Agent = "claude-code:land-agent"
	body.IdempotencyKey = "agent-fallback"
	var notice bytes.Buffer
	response, err := client.CreateRun(t.Context(), engine, body, &notice)
	if err != nil || response.RunID == "" {
		t.Fatalf("create run through an older controller: %v", err)
	}
	if posts.Load() != 2 || response.Inputs.Agent != "" {
		t.Fatalf("posts = %d, admitted agent %q; want one resend without the label", posts.Load(), response.Inputs.Agent)
	}
	if got := notice.String(); strings.Count(got, "\n") != 1 || !strings.Contains(got, "controller is older") || !strings.Contains(got, "claude-code:land-agent") {
		t.Fatalf("notice = %q", got)
	}

	posts.Store(0)
	notice.Reset()
	unlabeled := computeIntake("build")
	unlabeled.IdempotencyKey = "no-agent"
	if _, err := client.CreateRun(t.Context(), engine, unlabeled, &notice); err != nil || posts.Load() != 1 || notice.Len() != 0 {
		t.Fatalf("unlabeled request: err %v, posts %d, notice %q", err, posts.Load(), notice.String())
	}
}

func TestAgentFallbackLeavesOtherRefusalsAlone(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{})
	var posts atomic.Int32
	server := legacyController(t, f.h, &posts)
	engine := engineClientFor(t, server.URL, f.token)

	invalid := computeIntake("build")
	invalid.Request.Agent = "claude-code"
	invalid.Request.Pipeline = ""
	var notice bytes.Buffer
	_, err := client.CreateRun(t.Context(), engine, invalid, &notice)
	var refused *apierr.Error
	if !errors.As(err, &refused) || refused.Code != apierr.BadRequest || posts.Load() != 2 || notice.Len() != 0 {
		t.Fatalf("err %v, posts %d, notice %q; want the second refusal returned without a notice", err, posts.Load(), notice.String())
	}

	posts.Store(0)
	directServer := httptest.NewServer(f.h)
	t.Cleanup(directServer.Close)
	direct := engineClientFor(t, directServer.URL, f.token)
	malformed := computeIntake("build")
	malformed.Request.Agent = "bad label"
	if _, err := client.CreateRun(t.Context(), direct, malformed, &notice); !errors.As(err, &refused) || !strings.Contains(refused.Message, "agent label") || notice.Len() != 0 {
		t.Fatalf("an unsafe label must stay an error, got %v", err)
	}
}

func TestNewControllerAcceptsAnOlderClientAndNamesAFutureField(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{})
	body := computeIntake("build")
	legacy := reflect.New(legacyCreateRunType())
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, legacy.Interface()); err != nil {
		t.Fatal(err)
	}
	old, err := json.Marshal(legacy.Interface())
	if err != nil || bytes.Contains(old, []byte(`"agent"`)) {
		t.Fatalf("legacy body %s: %v", old, err)
	}
	post := func(payload []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/runs", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+f.token)
		return f.serve(req)
	}
	admitted := floodRun(t, post(old), http.StatusCreated)
	if admitted.Inputs.Agent != "" {
		t.Fatalf("older client's run gained agent %q", admitted.Inputs.Agent)
	}

	future := bytes.Replace(raw, []byte(`"request":{`), []byte(`"request":{"future_field":1,`), 1)
	w := post(future)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `field \"future_field\"`) || !strings.Contains(w.Body.String(), "upgrade the controller") || !strings.Contains(w.Body.String(), `"unknown_field":"future_field"`) {
		t.Fatalf("future field = %d %s", w.Code, w.Body)
	}
}
