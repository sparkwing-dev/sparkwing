package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestNativeChildAwaitReportsPlanningFailureBeforeReadingOutput(t *testing.T) {
	f := newStoreHostFixture(t, nodewire.PlanDoc{Jobs: []nodewire.JobSpec{{ID: "parent", Steps: []nodewire.StepSpec{{ID: "body"}}}}})
	now := time.Now()
	child := controllerwire.RunDetail{
		ID: "child", Status: "failed", Reason: "plan_failed", Failure: "unknown pipeline matrix-missing-child", FinishedAt: &now,
		Inputs: runspec.Display{Pipeline: "matrix-missing-child", Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1", Display: "acme/pipelines"}, SHA: "source-commit"}},
	}
	outputReads := 0
	original := controller.New(f.Store, nil).Handler()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/children/child"):
			if err := json.NewEncoder(w).Encode(child); err != nil {
				t.Error(err)
			}
		case strings.HasSuffix(r.URL.Path, "/children/child/jobs/body/output"):
			outputReads++
			w.WriteHeader(http.StatusNotFound)
			if err := json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "blob_missing", "message": "blob is absent or tombstoned"}}); err != nil {
				t.Error(err)
			}
		default:
			original.ServeHTTP(w, r)
		}
	})
	host, err := client.NewLocalHostClient(client.EngineOptions{URL: "http://controller", HTTP: &http.Client{Transport: ownerProcessTransport{handler}}, Bearer: func(context.Context) (string, error) { return string(f.OwnerBearer), nil }}, f.Input.RunID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Close(); err != nil {
			t.Error(err)
		}
	})
	principal, err := host.Principal(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cfg := WithClient(NativeConfig{}, host)
	services, err := cfg.Services(t.Context(), principal, nodewire.JobSpec{ID: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := services.AwaitChild(t.Context(), capability.Authorized{}, "child", "body", 0)
	if err != nil || result.Error == nil || result.Error.Code != apierr.PlanFailed || outputReads != 0 {
		t.Fatalf("child planning failure: result=%+v error=%v output reads=%d", result, err, outputReads)
	}
	for _, want := range []string{"matrix-missing-child", "acme/pipelines", "source-commit", "unknown pipeline"} {
		if !strings.Contains(result.Error.Message, want) {
			t.Errorf("missing %q in %q", want, result.Error.Message)
		}
	}
	if !strings.Contains(result.Error.Message, "Check the pipeline name") || !strings.Contains(result.Error.Message, "source") {
		t.Fatalf("no repair advice: %+v", result.Error)
	}

	child.Status, child.Reason, child.Failure = "success", "", ""
	_, err = services.AwaitChild(t.Context(), capability.Authorized{}, "child", "body", 0)
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Code != apierr.Code("blob_missing") || outputReads != 1 {
		t.Fatalf("successful child lost output failure: error=%v output reads=%d", err, outputReads)
	}
}
