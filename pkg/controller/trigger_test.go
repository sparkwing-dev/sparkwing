package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/inprocdispatch"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// TestTrigger_Validation confirms malformed payloads fail fast
// without consulting the dispatcher.
func TestTrigger_Validation(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	capture := &captureDispatcher{}
	srvController := controller.New(st, nil)
	srvController.WithDispatcher(capture)
	srv := httptest.NewServer(srvController.Handler())
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/api/v1/triggers", map[string]string{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status=%d want 400", resp.StatusCode)
	}
	_ = resp.Body.Close()

	resp2 := postJSON(t, srv.URL+"/api/v1/triggers", map[string]any{
		"pipeline": "demo",
		"unknown":  true,
	})
	if resp2.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown field status=%d want 400", resp2.StatusCode)
	}
	_ = resp2.Body.Close()
}

// A POST without trigger.source gets 400, not a 202 with a
// mislabeled default source.
func TestTrigger_MissingSource400(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	capture := &captureDispatcher{}
	srvController := controller.New(st, nil)
	srvController.WithDispatcher(capture)
	srv := httptest.NewServer(srvController.Handler())
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/api/v1/triggers", map[string]any{
		"pipeline": "demo",
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status=%d want 400 (missing trigger.source)", resp.StatusCode)
	}
}

// TestTrigger_NoopDispatcher exercises the default path: controller
// accepts the trigger, returns a run_id, but no pipeline actually
// runs. Proves the handler returns quickly regardless of dispatch
// behavior.
func TestTrigger_NoopDispatcher(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	capture := &captureDispatcher{}
	srvController := controller.New(st, nil)
	srvController.WithDispatcher(capture)
	srv := httptest.NewServer(srvController.Handler())
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/api/v1/triggers", map[string]any{
		"pipeline": "demo",
		"trigger":  map[string]string{"source": "github"},
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d want 202 (body: %s)", resp.StatusCode, body)
	}
	var body struct {
		RunID  string `json:"run_id"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.RunID == "" {
		t.Error("run_id empty")
	}
	if body.Status != "dispatched" {
		t.Errorf("status=%q want dispatched", body.Status)
	}
}

func TestTrigger_StripsClientSuppliedPlanAdmissionEnv(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	capture := &captureDispatcher{}
	srvController := controller.New(st, nil)
	srvController.WithDispatcher(capture)
	srv := httptest.NewServer(srvController.Handler())
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/api/v1/triggers", map[string]any{
		"pipeline": "demo",
		"trigger": map[string]any{
			"source": "manual",
			"env": map[string]string{
				"SPARKWING_PLAN_ADMISSION_KEY":       "cache-key",
				"SPARKWING_PLAN_ADMISSION_HOLDER_ID": "forged/-",
				"SAFE":                               "kept",
			},
		},
	})
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d want 202 (body: %s)", resp.StatusCode, body)
	}
	var body struct {
		RunID string `json:"run_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	trigger, err := st.GetTrigger(context.Background(), body.RunID)
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}
	if trigger.TriggerEnv["SPARKWING_PLAN_ADMISSION_KEY"] != "" {
		t.Fatalf("reserved admission key persisted from public env: %+v", trigger.TriggerEnv)
	}
	if trigger.TriggerEnv["SPARKWING_PLAN_ADMISSION_HOLDER_ID"] != "" {
		t.Fatalf("reserved admission holder persisted from public env: %+v", trigger.TriggerEnv)
	}
	if trigger.TriggerEnv["SAFE"] != "kept" {
		t.Fatalf("safe env = %q, want kept", trigger.TriggerEnv["SAFE"])
	}
}

func TestTrigger_PlanAdmissionRequiresActiveParentHolder(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID:        "parent-run",
		Pipeline:  "parent",
		Status:    "running",
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	_, err = st.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
		Key:      "cache-key",
		HolderID: "parent-run/-",
		RunID:    "parent-run",
		Capacity: 1,
		Policy:   store.OnLimitQueue,
	})
	if err != nil {
		t.Fatalf("AcquireConcurrencySlot: %v", err)
	}

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/api/v1/triggers", map[string]any{
		"pipeline":      "child",
		"parent_run_id": "parent-run",
		"trigger":       map[string]string{"source": "await-pipeline"},
		"plan_admission": map[string]string{
			"key":       "cache-key",
			"holder_id": "parent-run/-",
		},
	})
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d want 202 (body: %s)", resp.StatusCode, body)
	}
	var body struct {
		RunID string `json:"run_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	trigger, err := st.GetTrigger(ctx, body.RunID)
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}
	if trigger.TriggerEnv["SPARKWING_PLAN_ADMISSION_KEY"] != "cache-key" {
		t.Fatalf("admission key = %q, want cache-key", trigger.TriggerEnv["SPARKWING_PLAN_ADMISSION_KEY"])
	}
	if trigger.TriggerEnv["SPARKWING_PLAN_ADMISSION_HOLDER_ID"] != "parent-run/-" {
		t.Fatalf("admission holder = %q, want parent-run/-", trigger.TriggerEnv["SPARKWING_PLAN_ADMISSION_HOLDER_ID"])
	}

	stale := postJSON(t, srv.URL+"/api/v1/triggers", map[string]any{
		"pipeline":      "child",
		"parent_run_id": "parent-run",
		"trigger":       map[string]string{"source": "await-pipeline"},
		"plan_admission": map[string]string{
			"key":       "missing-key",
			"holder_id": "parent-run/-",
		},
	})
	if stale.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(stale.Body)
		t.Fatalf("stale status=%d want 400 (body: %s)", stale.StatusCode, body)
	}
}

func TestTrigger_PlanAdmissionAcceptsAncestorHolder(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	for _, run := range []store.Run{
		{ID: "grandparent-run", Pipeline: "grandparent", Status: "running", StartedAt: time.Now()},
		{ID: "parent-run", Pipeline: "parent", Status: "running", ParentRunID: "grandparent-run", StartedAt: time.Now()},
	} {
		if err := st.CreateRun(ctx, run); err != nil {
			t.Fatalf("CreateRun(%s): %v", run.ID, err)
		}
	}
	_, err = st.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
		Key:      "cache-key",
		HolderID: "grandparent-run/-",
		RunID:    "grandparent-run",
		Capacity: 1,
		Policy:   store.OnLimitQueue,
	})
	if err != nil {
		t.Fatalf("AcquireConcurrencySlot: %v", err)
	}

	capture := &captureDispatcher{}
	srv := controller.New(st, nil)
	srv.WithDispatcher(capture)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := postJSON(t, ts.URL+"/api/v1/triggers", map[string]any{
		"pipeline":      "child",
		"parent_run_id": "parent-run",
		"trigger":       map[string]string{"source": "await-pipeline"},
		"plan_admission": map[string]string{
			"key":       "cache-key",
			"holder_id": "grandparent-run/-",
		},
	})
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d want 202 (body: %s)", resp.StatusCode, body)
	}
	var body struct {
		RunID string `json:"run_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	trigger, err := st.GetTrigger(ctx, body.RunID)
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}
	if trigger.TriggerEnv["SPARKWING_PLAN_ADMISSION_KEY"] != "cache-key" {
		t.Fatalf("admission key = %q, want cache-key", trigger.TriggerEnv["SPARKWING_PLAN_ADMISSION_KEY"])
	}
	if trigger.TriggerEnv["SPARKWING_PLAN_ADMISSION_HOLDER_ID"] != "grandparent-run/-" {
		t.Fatalf("admission holder = %q, want grandparent-run/-", trigger.TriggerEnv["SPARKWING_PLAN_ADMISSION_HOLDER_ID"])
	}
	capture.mu.Lock()
	got := capture.last
	capture.mu.Unlock()
	if got.InheritedPlanConcurrencyKey != "cache-key" {
		t.Fatalf("dispatcher admission key = %q, want cache-key", got.InheritedPlanConcurrencyKey)
	}
	if got.InheritedPlanConcurrencyHolderID != "grandparent-run/-" {
		t.Fatalf("dispatcher admission holder = %q, want grandparent-run/-", got.InheritedPlanConcurrencyHolderID)
	}
}

func TestTrigger_PlanAdmissionAcceptsAdmissionSet(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	for _, run := range []store.Run{
		{ID: "ancestor-run", Pipeline: "ancestor", Status: "running", StartedAt: time.Now()},
		{ID: "middle-run", Pipeline: "middle", Status: "running", ParentRunID: "ancestor-run", StartedAt: time.Now()},
	} {
		if err := st.CreateRun(ctx, run); err != nil {
			t.Fatalf("CreateRun(%s): %v", run.ID, err)
		}
	}
	for _, req := range []store.AcquireSlotRequest{
		{
			Key:      "g:ancestor-key",
			HolderID: "ancestor-run/-",
			RunID:    "ancestor-run",
			Capacity: 1,
			Policy:   store.OnLimitQueue,
		},
		{
			Key:      "g:middle-key",
			HolderID: "middle-run/-",
			RunID:    "middle-run",
			Capacity: 1,
			Policy:   store.OnLimitQueue,
		},
	} {
		resp, err := st.AcquireConcurrencySlot(ctx, req)
		if err != nil {
			t.Fatalf("AcquireConcurrencySlot(%s): %v", req.Key, err)
		}
		if resp.Kind != store.AcquireGranted {
			t.Fatalf("AcquireConcurrencySlot(%s) = %s, want granted", req.Key, resp.Kind)
		}
	}

	capture := &captureDispatcher{}
	srvController := controller.New(st, nil)
	srvController.WithDispatcher(capture)
	srv := httptest.NewServer(srvController.Handler())
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/api/v1/triggers", map[string]any{
		"pipeline":      "grandchild",
		"parent_run_id": "middle-run",
		"trigger":       map[string]string{"source": "await-pipeline"},
		"plan_admission": map[string]any{
			"key":       "g:middle-key",
			"holder_id": "middle-run/-",
			"admissions": map[string]string{
				"g:ancestor-key": "ancestor-run/-",
				"g:middle-key":   "middle-run/-",
			},
		},
	})
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d want 202 (body: %s)", resp.StatusCode, body)
	}
	var body struct {
		RunID string `json:"run_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	trigger, err := st.GetTrigger(ctx, body.RunID)
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}
	var admissions map[string]string
	if err := json.Unmarshal([]byte(trigger.TriggerEnv["SPARKWING_PLAN_ADMISSIONS"]), &admissions); err != nil {
		t.Fatalf("unmarshal plan admissions: %v", err)
	}
	if admissions["g:ancestor-key"] != "ancestor-run/-" {
		t.Fatalf("ancestor admission = %q, want ancestor-run/-", admissions["g:ancestor-key"])
	}
	if admissions["g:middle-key"] != "middle-run/-" {
		t.Fatalf("middle admission = %q, want middle-run/-", admissions["g:middle-key"])
	}
	capture.mu.Lock()
	got := capture.last
	capture.mu.Unlock()
	if got.InheritedPlanConcurrencyHolders["g:ancestor-key"] != "ancestor-run/-" {
		t.Fatalf("dispatcher ancestor admission = %q, want ancestor-run/-",
			got.InheritedPlanConcurrencyHolders["g:ancestor-key"])
	}
	if got.InheritedPlanConcurrencyHolders["g:middle-key"] != "middle-run/-" {
		t.Fatalf("dispatcher middle admission = %q, want middle-run/-",
			got.InheritedPlanConcurrencyHolders["g:middle-key"])
	}
}

func TestTrigger_PlanAdmissionRejectsNodeLevelHolder(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID:        "parent-run",
		Pipeline:  "parent",
		Status:    "running",
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	_, err = st.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
		Key:      "cache-key",
		HolderID: "parent-run/build",
		RunID:    "parent-run",
		NodeID:   "build",
		Capacity: 1,
		Policy:   store.OnLimitQueue,
	})
	if err != nil {
		t.Fatalf("AcquireConcurrencySlot: %v", err)
	}

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/api/v1/triggers", map[string]any{
		"pipeline":      "child",
		"parent_run_id": "parent-run",
		"trigger":       map[string]string{"source": "await-pipeline"},
		"plan_admission": map[string]string{
			"key":       "cache-key",
			"holder_id": "parent-run/build",
		},
	})
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d want 400 (body: %s)", resp.StatusCode, body)
	}
}

func TestTrigger_PlanAdmissionRejectsUnverifiedHostAdmission(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID:           "parent-run",
		Pipeline:     "parent",
		Status:       "running",
		StartedAt:    time.Now(),
		PlanSnapshot: []byte(`{"plan_concurrency":{"key":"cache-key","host_admission":false}}`),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	_, err = st.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
		Key:      "cache-key",
		HolderID: "parent-run/-",
		RunID:    "parent-run",
		Capacity: 1,
		Policy:   store.OnLimitQueue,
	})
	if err != nil {
		t.Fatalf("AcquireConcurrencySlot: %v", err)
	}

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/api/v1/triggers", map[string]any{
		"pipeline":      "child",
		"parent_run_id": "parent-run",
		"trigger":       map[string]string{"source": "await-pipeline"},
		"plan_admission": map[string]any{
			"key":            "cache-key",
			"holder_id":      "parent-run/-",
			"host_admission": true,
		},
	})
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d want 400 (body: %s)", resp.StatusCode, body)
	}
}

func TestTrigger_PlanAdmissionAcceptsVerifiedHostAdmission(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID:           "parent-run",
		Pipeline:     "parent",
		Status:       "running",
		StartedAt:    time.Now(),
		PlanSnapshot: []byte(`{"plan_concurrency":{"key":"cache-key","host_admission":true}}`),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	_, err = st.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
		Key:      "cache-key",
		HolderID: "parent-run/-",
		RunID:    "parent-run",
		Capacity: 1,
		Policy:   store.OnLimitQueue,
	})
	if err != nil {
		t.Fatalf("AcquireConcurrencySlot: %v", err)
	}

	capture := &captureDispatcher{}
	srvController := controller.New(st, nil)
	srvController.WithDispatcher(capture)
	srv := httptest.NewServer(srvController.Handler())
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/api/v1/triggers", map[string]any{
		"pipeline":      "child",
		"parent_run_id": "parent-run",
		"trigger":       map[string]string{"source": "await-pipeline"},
		"plan_admission": map[string]any{
			"key":            "cache-key",
			"holder_id":      "parent-run/-",
			"host_admission": true,
		},
	})
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d want 202 (body: %s)", resp.StatusCode, body)
	}
	capture.mu.Lock()
	got := capture.last
	capture.mu.Unlock()
	if !got.InheritedPlanHostAdmission {
		t.Fatalf("dispatcher InheritedPlanHostAdmission = false, want true")
	}
	trigger, err := st.GetTrigger(ctx, got.RunID)
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}
	if trigger.TriggerEnv["SPARKWING_PLAN_HOST_ADMISSION"] != "1" {
		t.Fatalf("trigger host admission env = %q, want 1", trigger.TriggerEnv["SPARKWING_PLAN_HOST_ADMISSION"])
	}
	if trigger.TriggerEnv["SPARKWING_PLAN_HOST_ADMISSION_KEY"] != "cache-key" {
		t.Fatalf("trigger host admission key env = %q, want cache-key", trigger.TriggerEnv["SPARKWING_PLAN_HOST_ADMISSION_KEY"])
	}
	if got.InheritedPlanHostAdmissionKey != "cache-key" {
		t.Fatalf("dispatcher InheritedPlanHostAdmissionKey = %q, want cache-key", got.InheritedPlanHostAdmissionKey)
	}
}

func TestTrigger_PlanAdmissionRejectsTerminalHostAdmissionOwner(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID:           "parent-run",
		Pipeline:     "parent",
		Status:       "running",
		StartedAt:    time.Now(),
		PlanSnapshot: []byte(`{"plan_concurrency":{"key":"cache-key","host_admission":true}}`),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	_, err = st.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
		Key:      "cache-key",
		HolderID: "parent-run/-",
		RunID:    "parent-run",
		Capacity: 1,
		Policy:   store.OnLimitQueue,
	})
	if err != nil {
		t.Fatalf("AcquireConcurrencySlot: %v", err)
	}
	if err := st.FinishRun(ctx, "parent-run", "success", ""); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	capture := &captureDispatcher{}
	srvController := controller.New(st, nil)
	srvController.WithDispatcher(capture)
	srv := httptest.NewServer(srvController.Handler())
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/api/v1/triggers", map[string]any{
		"pipeline":      "child",
		"parent_run_id": "parent-run",
		"trigger":       map[string]string{"source": "await-pipeline"},
		"plan_admission": map[string]any{
			"key":            "cache-key",
			"holder_id":      "parent-run/-",
			"host_admission": true,
		},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d want 400 (body: %s)", resp.StatusCode, body)
	}
}

func TestTrigger_PlanAdmissionAcceptsMixedHostAndNonHostAdmissions(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID:           "parent-run",
		Pipeline:     "parent",
		Status:       "running",
		StartedAt:    time.Now(),
		PlanSnapshot: []byte(`{"plan_concurrency":{"key":"host-key","host_admission":true}}`),
	}); err != nil {
		t.Fatalf("CreateRun(parent): %v", err)
	}
	if err := st.CreateRun(ctx, store.Run{
		ID:           "middle-run",
		Pipeline:     "middle",
		Status:       "running",
		ParentRunID:  "parent-run",
		StartedAt:    time.Now(),
		PlanSnapshot: []byte(`{"plan_concurrency":{"key":"other-key","host_admission":false}}`),
	}); err != nil {
		t.Fatalf("CreateRun(middle): %v", err)
	}
	for _, req := range []store.AcquireSlotRequest{
		{Key: "host-key", HolderID: "parent-run/-", RunID: "parent-run", Capacity: 1, Policy: store.OnLimitQueue},
		{Key: "other-key", HolderID: "middle-run/-", RunID: "middle-run", Capacity: 1, Policy: store.OnLimitQueue},
	} {
		if _, err := st.AcquireConcurrencySlot(ctx, req); err != nil {
			t.Fatalf("AcquireConcurrencySlot(%s): %v", req.Key, err)
		}
	}

	capture := &captureDispatcher{}
	srvController := controller.New(st, nil)
	srvController.WithDispatcher(capture)
	srv := httptest.NewServer(srvController.Handler())
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/api/v1/triggers", map[string]any{
		"pipeline":      "child",
		"parent_run_id": "middle-run",
		"trigger":       map[string]string{"source": "await-pipeline"},
		"plan_admission": map[string]any{
			"admissions": map[string]string{
				"host-key":  "parent-run/-",
				"other-key": "middle-run/-",
			},
			"host_admission":     true,
			"host_admission_key": "host-key",
		},
	})
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d want 202 (body: %s)", resp.StatusCode, body)
	}
	capture.mu.Lock()
	got := capture.last
	capture.mu.Unlock()
	if !got.InheritedPlanHostAdmission || got.InheritedPlanHostAdmissionKey != "host-key" {
		t.Fatalf("dispatcher host admission = %v/%q, want true/host-key", got.InheritedPlanHostAdmission, got.InheritedPlanHostAdmissionKey)
	}
	if got.InheritedPlanConcurrencyHolders["other-key"] != "middle-run/-" {
		t.Fatalf("dispatcher holders = %+v, want other-key preserved", got.InheritedPlanConcurrencyHolders)
	}
}

// TestTrigger_InProcessDispatcher_FullLoop is the full vertical
// slice: webhook arrives, controller dispatches, pipeline runs
// against the same controller via HTTP, final state lands in the
// DB. Proves external triggers actually produce completed runs.
func TestTrigger_InProcessDispatcher_FullLoop(t *testing.T) {
	registerPipeline("trigger-e2e", func() sparkwing.Pipeline[sparkwing.NoInputs] { return triggerE2EPipe{} })

	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	srv := controller.New(st, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	paths := orchestrator.PathsAt(dir)
	if err := paths.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	local := orchestrator.LocalBackends(paths, st, nil)
	backends := orchestrator.Backends{
		State:       client.New(ts.URL, nil),
		Logs:        local.Logs,
		Concurrency: local.Concurrency,
	}
	srv.WithDispatcher(inprocdispatch.InProcessDispatcher{Backends: backends})

	resp := postJSON(t, ts.URL+"/api/v1/triggers", map[string]any{
		"pipeline": "trigger-e2e",
		"trigger":  map[string]string{"source": "github"},
		"git":      map[string]string{"branch": "main", "sha": "abc123"},
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("trigger status=%d want 202 (body: %s)", resp.StatusCode, body)
	}
	var body struct {
		RunID string `json:"run_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.RunID == "" {
		t.Fatal("empty run_id")
	}

	deadline := time.Now().Add(3 * time.Second)
	var finalRun *store.Run
	for time.Now().Before(deadline) {
		run, err := st.GetRun(context.Background(), body.RunID)
		if err == nil && run.FinishedAt != nil {
			finalRun = run
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if finalRun == nil {
		t.Fatalf("run %s never finished within deadline", body.RunID)
	}
	if finalRun.Status != "success" {
		t.Errorf("run status=%q want success (err=%q)", finalRun.Status, finalRun.Error)
	}
	if finalRun.TriggerSource != "github" {
		t.Errorf("trigger_source=%q want github", finalRun.TriggerSource)
	}

	nodes, err := st.ListNodes(context.Background(), body.RunID)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 1 {
		t.Errorf("nodes=%d want 1", len(nodes))
	}
	if nodes[0].Outcome != string(sparkwing.Success) {
		t.Errorf("node outcome=%q want success", nodes[0].Outcome)
	}
}

// every accepted trigger creates a pending Run row so
// `runs list` / `runs status` show it before the runner has even
// claimed it. Without this, dispatches that fail at fetch / compile
// would never surface in the CLI.
func TestTrigger_CreatesPendingRunRow(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/api/v1/triggers", map[string]any{
		"pipeline": "demo",
		"trigger":  map[string]string{"source": "github"},
		"git":      map[string]string{"branch": "main", "sha": "deadbeef"},
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d want 202 (body: %s)", resp.StatusCode, body)
	}
	var body struct {
		RunID string `json:"run_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.RunID == "" {
		t.Fatal("empty run_id")
	}

	run, err := st.GetRun(context.Background(), body.RunID)
	if err != nil {
		t.Fatalf("GetRun(%s): %v", body.RunID, err)
	}
	if run.Status != "pending" {
		t.Errorf("Status=%q want pending", run.Status)
	}
	if run.Pipeline != "demo" {
		t.Errorf("Pipeline=%q want demo", run.Pipeline)
	}
	if run.GitSHA != "deadbeef" {
		t.Errorf("GitSHA=%q want deadbeef", run.GitSHA)
	}
	if run.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero")
	}
	runs, err := st.ListRuns(context.Background(), store.RunFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	found := false
	for _, r := range runs {
		if r.ID == body.RunID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("ListRuns did not include the pending run %s", body.RunID)
	}
}

// a controller-pre-allocated pending row gets transitioned
// to running when the orchestrator's CreateRun fires. This is the
// claimed -> running edge. The upsert deliberately
// preserves the original CreatedAt so receipt fields can
// reason about queue latency = StartedAt - CreatedAt.
func TestTrigger_PendingTransitionsToRunning(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	ctx := context.Background()
	created := time.Now().Add(-time.Hour)
	if err := st.CreateRun(ctx, store.Run{
		ID:        "run-pending-1",
		Pipeline:  "demo",
		Status:    "pending",
		CreatedAt: created,
		StartedAt: created,
	}); err != nil {
		t.Fatalf("CreateRun pending: %v", err)
	}

	started := time.Now()
	if err := st.CreateRun(ctx, store.Run{
		ID:        "run-pending-1",
		Pipeline:  "demo",
		Status:    "running",
		StartedAt: started,
	}); err != nil {
		t.Fatalf("CreateRun running upsert: %v", err)
	}

	got, err := st.GetRun(ctx, "run-pending-1")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Status != "running" {
		t.Errorf("Status=%q want running", got.Status)
	}
	if got.CreatedAt.Truncate(time.Second) != created.Truncate(time.Second) {
		t.Errorf("CreatedAt=%v want %v (lost on upsert)", got.CreatedAt, created)
	}
}

// TestTrigger_DispatcherError surfaces dispatcher-reported errors
// as 500 responses so the caller can retry.
func TestTrigger_DispatcherError(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	srv := controller.New(st, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	srv.WithDispatcher(&failingDispatcher{})

	resp := postJSON(t, ts.URL+"/api/v1/triggers", map[string]any{
		"pipeline": "x",
		"trigger":  map[string]string{"source": "manual"},
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status=%d want 500", resp.StatusCode)
	}
}

type triggerE2EPipe struct{ sparkwing.Base }

func (triggerE2EPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "work", func(ctx context.Context) error {
		sparkwing.Info(ctx, "work via webhook trigger")
		return nil
	})
	return nil
}

type failingDispatcher struct {
	called atomic.Int32
}

func (f *failingDispatcher) Dispatch(_ context.Context, _ controller.RunRequest) error {
	f.called.Add(1)
	return errors.New("dispatcher broken")
}

type captureDispatcher struct {
	mu   sync.Mutex
	last controller.RunRequest
}

func (c *captureDispatcher) Dispatch(_ context.Context, req controller.RunRequest) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.last = req
	return nil
}

// registerPipeline defined in e2e_test context via the client package,
// but that's a different test package. Redefine locally.
var registerOnce sync.Map

func registerPipeline(name string, factory func() sparkwing.Pipeline[sparkwing.NoInputs]) {
	if _, loaded := registerOnce.LoadOrStore(name, struct{}{}); loaded {
		return
	}
	sparkwing.Register[sparkwing.NoInputs](name, factory)
}

func postJSON(t *testing.T, url string, body any) *http.Response {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}
