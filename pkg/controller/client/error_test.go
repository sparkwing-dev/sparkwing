package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/executionpolicy"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestReadHTTPErrorMapsMaskedNotImplementedToStorageSentinel(t *testing.T) {
	resp := &http.Response{
		Status:     "501 Not Implemented",
		StatusCode: http.StatusNotImplemented,
		Body:       io.NopCloser(strings.NewReader(`{"error":"internal server error"}`)),
	}
	err := readHTTPError(resp)
	if !errors.Is(err, storage.ErrNotSupported) {
		t.Fatalf("error = %v, want storage.ErrNotSupported", err)
	}
	if strings.Contains(err.Error(), "internal server error") {
		t.Fatalf("error copied masked response body: %v", err)
	}
}

func TestReadHTTPErrorPreservesAssistedAdmissionTypesBeforeGenericConflict(t *testing.T) {
	for name, body := range map[string]string{
		"upgrade":  `{"code":"upgrade_required","scope":"supervisor","missing":["future-supervisor-v9"],"safe_hold":true}`,
		"protocol": `{"code":"protocol_incompatible","policy_protocol":1,"helper_minimum":2,"helper_maximum":3}`,
		"body":     `{"code":"body_attestation_required","run_id":"run","node_id":"node"}`,
	} {
		t.Run(name, func(t *testing.T) {
			resp := &http.Response{Status: "409 Conflict", StatusCode: http.StatusConflict, Body: io.NopCloser(strings.NewReader(body))}
			err := readHTTPError(resp)
			switch name {
			case "upgrade":
				var typed *executionpolicy.UpgradeRequiredError
				if !errors.As(err, &typed) || typed.Scope != "supervisor" || typed.MinimumRelease != "" ||
					!typed.SafeHold || !reflect.DeepEqual(typed.Missing, []string{"future-supervisor-v9"}) {
					t.Fatalf("upgrade error = %#v, %v", typed, err)
				}
			case "protocol":
				var typed *executionpolicy.ProtocolIncompatibleError
				if !errors.As(err, &typed) || typed.PolicyProtocol != 1 || typed.HelperMinimum != 2 {
					t.Fatalf("protocol error = %#v, %v", typed, err)
				}
			case "body":
				var typed *executionpolicy.BodyAttestationRequiredError
				if !errors.As(err, &typed) || typed.RunID != "run" || typed.NodeID != "node" {
					t.Fatalf("body error = %#v, %v", typed, err)
				}
			}
			if errors.Is(err, store.ErrLockHeld) {
				t.Fatalf("typed assisted error collapsed to lock-held: %v", err)
			}
		})
	}
	resp := &http.Response{Status: "409 Conflict", StatusCode: http.StatusConflict, Body: io.NopCloser(strings.NewReader(`{"error":"held"}`))}
	if err := readHTTPError(resp); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("ordinary conflict error = %v, want lock held", err)
	}
}

func TestPrepareExecutorClaimOwnsPrivateBindingEvenWhenBodyAttestationRefuses(t *testing.T) {
	binding := executionpolicy.ClaimBinding{
		RunID: "run", NodeID: "node", PolicyHash: "sha256:policy", PolicyVersion: 1, BodyProtocol: 1,
		SupervisorRequirementsHash: "sha256:supervisor", BodyRequirementsHash: "sha256:body",
	}
	rawBinding, err := executionpolicy.EncodeClaimBinding(binding)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/nodes/claim/prepare" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": "body_attestation_required", "run_id": "run", "node_id": "node",
			"execution_binding": json.RawMessage(rawBinding),
		})
	}))
	defer srv.Close()
	sink := executionpolicy.NewPreparationSink()
	ctx := executionpolicy.WithPreparationSink(context.Background(), sink)
	_, err = New(srv.URL, nil).PrepareExecutorClaim(ctx, "helper")
	if !errors.Is(err, executionpolicy.ErrBodyAttestationRequired) {
		t.Fatalf("prepare error = %v, want body attestation", err)
	}
	if got := sink.Load(); got != binding {
		t.Fatalf("private prepared binding = %+v, want %+v", got, binding)
	}
}
