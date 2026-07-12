package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func sampleQueueState() wingwire.QueueState {
	return wingwire.QueueState{
		Resources: []wingwire.ResourceState{
			{Key: "cores", Capacity: 8, Held: 6},
			{Key: "memory", Capacity: 16 << 30, Held: 4 << 30},
			{Key: "deploy-lock", Capacity: 1, Held: 1},
		},
		Holders: []wingwire.Holder{
			{RunID: "run-holder", Pipeline: "deploy", ElapsedMS: 90000,
				Resources: wingwire.HostResources{Cores: 6, MemoryBytes: 4 << 30}, Semaphores: []string{"deploy-lock"}},
		},
		Waiters: []wingwire.Waiter{
			{RunID: "run-waiter", Pipeline: "build", Position: 1,
				Resources: wingwire.HostResources{Cores: 4}, WaitingOn: []string{"cores"}, WaitingMS: 12000},
		},
	}
}

func TestRenderQueue_PrettyShowsOrigin(t *testing.T) {
	qs := wingwire.QueueState{
		Holders: []wingwire.Holder{
			{RunID: "local-run", Pipeline: "build", Origin: wingwire.OriginLocal,
				Resources: wingwire.HostResources{Cores: 2}},
			{RunID: "ctrl-run", Pipeline: "deploy", Origin: wingwire.OriginController,
				Resources: wingwire.HostResources{Cores: 4}},
		},
		Waiters: []wingwire.Waiter{
			{RunID: "ctrl-waiter", Pipeline: "test", Position: 1, Origin: wingwire.OriginController,
				Resources: wingwire.HostResources{Cores: 8}},
		},
	}
	var buf bytes.Buffer
	if err := renderQueue(&buf, qs, "pretty"); err != nil {
		t.Fatalf("render pretty: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "ORIGIN") {
		t.Errorf("pretty view missing ORIGIN column:\n%s", out)
	}
	if !strings.Contains(out, "controller") {
		t.Errorf("controller origin not rendered:\n%s", out)
	}
	if !strings.Contains(out, "local") {
		t.Errorf("local origin not rendered:\n%s", out)
	}
}

func TestOriginWord_EmptyIsLocal(t *testing.T) {
	if got := originWord(""); got != "local" {
		t.Errorf(`originWord(""): got %q want "local"`, got)
	}
	if got := originWord(wingwire.OriginController); got != "controller" {
		t.Errorf("originWord(controller): got %q want controller", got)
	}
}

func TestRenderQueue_IgnoreExternalLabeledInAllModes(t *testing.T) {
	qs := sampleQueueState()
	qs.IgnoreExternal = true

	pretty := renderQueueTo(t, qs, "pretty")
	if !strings.Contains(pretty, "external: ignored (operator setting)") {
		t.Errorf("pretty view missing the ignored-external line:\n%s", pretty)
	}

	plain := renderQueueTo(t, qs, "plain")
	if !strings.Contains(plain, "external\tignored") {
		t.Errorf("plain view missing the ignored-external record:\n%s", plain)
	}

	jsonOut := renderQueueTo(t, qs, "json")
	var got wingwire.QueueState
	if err := json.Unmarshal([]byte(jsonOut), &got); err != nil {
		t.Fatalf("json invalid: %v\n%s", err, jsonOut)
	}
	if !got.IgnoreExternal {
		t.Errorf("json dropped ignore_external field:\n%s", jsonOut)
	}
}

// TestRenderQueue_IgnoreExternalSuppressesPressureNote pins that the
// "external is the binding constraint" callout never fires when the
// operator has told admission to ignore external load -- it would blame a
// constraint the daemon is no longer enforcing.
func TestRenderQueue_IgnoreExternalSuppressesPressureNote(t *testing.T) {
	qs := wingwire.QueueState{
		IgnoreExternal: true,
		Resources: []wingwire.ResourceState{
			{Key: "cores", Capacity: 8, Held: 2, External: 5},
		},
		Waiters: []wingwire.Waiter{
			{RunID: "w", Position: 1, Resources: wingwire.HostResources{Cores: 4},
				BlockingReason: "needs 4.0 cores; 1.0 available"},
		},
	}
	if note := externalPressureNote(qs); note != "" {
		t.Errorf("external pressure note should be suppressed under ignore-external, got %q", note)
	}
}

func renderQueueTo(t *testing.T, qs wingwire.QueueState, format string) string {
	t.Helper()
	var buf bytes.Buffer
	if err := renderQueue(&buf, qs, format); err != nil {
		t.Fatalf("render %s: %v", format, err)
	}
	return buf.String()
}

func TestRenderQueue_JSONRoundTrips(t *testing.T) {
	want := sampleQueueState()
	var buf bytes.Buffer
	if err := renderQueue(&buf, want, "json"); err != nil {
		t.Fatalf("render json: %v", err)
	}
	var got wingwire.QueueState
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("json is not valid: %v\n%s", err, buf.String())
	}
	if len(got.Resources) != 3 || len(got.Holders) != 1 || len(got.Waiters) != 1 {
		t.Fatalf("json lost rows: %+v", got)
	}
	if got.Waiters[0].Position != 1 || got.Waiters[0].WaitingOn[0] != "cores" {
		t.Fatalf("waiter fields not preserved: %+v", got.Waiters[0])
	}
}

func TestRenderQueue_JSONToleratesUnknownFields(t *testing.T) {
	raw := `{"resources":[{"key":"cores","capacity":8,"held":2,"eta_seconds":42}],
		"waiters":[{"run_id":"r","position":1,"expected_start_ms":9000}]}`
	var qs wingwire.QueueState
	if err := json.Unmarshal([]byte(raw), &qs); err != nil {
		t.Fatalf("unknown extra fields should decode cleanly: %v", err)
	}
	if len(qs.Resources) != 1 || qs.Waiters[0].Position != 1 {
		t.Fatalf("known fields lost alongside unknown ones: %+v", qs)
	}
}

func TestRenderQueue_PrettyShowsHoldersWaitersAndCapacity(t *testing.T) {
	var buf bytes.Buffer
	if err := renderQueue(&buf, sampleQueueState(), "pretty"); err != nil {
		t.Fatalf("render pretty: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"1 holding, 1 queued",
		"RESOURCE", "cores", "deploy-lock",
		"RUN", "run-holder", "deploy",
		"POS", "run-waiter", "cores",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("pretty output missing %q:\n%s", want, out)
		}
	}
}

func TestRenderQueue_PrettyExplainsHostPressureWait(t *testing.T) {
	qs := wingwire.QueueState{
		Resources: []wingwire.ResourceState{
			{Key: "cores", Capacity: 10, Held: 0, Reserved: 2, External: 3.2, Available: 4.8},
		},
		Waiters: []wingwire.Waiter{
			{RunID: "run-waiter", Position: 1, Resources: wingwire.HostResources{Cores: 5},
				WaitingOn: []string{"cores"}, BlockingReason: "needs 5.0 cores; 4.8 available (external load 3.2)"},
		},
	}
	var buf bytes.Buffer
	if err := renderQueue(&buf, qs, "pretty"); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"RESERVED", "EXTERNAL", "AVAILABLE",
		"needs 5.0 cores; 4.8 available (external load 3.2)",
		"external (non-sparkwing) load is the binding constraint",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("pretty output missing %q:\n%s", want, out)
		}
	}
}

// TestRenderQueue_PrettyToleratesOlderDaemonWithoutHeadroom pins that a
// queue payload with no headroom fields (an older daemon) still renders a
// sane AVAILABLE column from capacity minus held.
func TestRenderQueue_PrettyToleratesOlderDaemonWithoutHeadroom(t *testing.T) {
	var buf bytes.Buffer
	if err := renderQueue(&buf, sampleQueueState(), "pretty"); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "AVAILABLE") {
		t.Fatalf("AVAILABLE column missing:\n%s", out)
	}
	if strings.Contains(out, "binding constraint") {
		t.Fatalf("no external field should mean no external-pressure note:\n%s", out)
	}
}

func TestRenderQueue_PrettyFlagsStalledHolderWithRecovery(t *testing.T) {
	qs := sampleQueueState()
	qs.Holders[0].Stalled = true
	qs.Holders[0].Recovery = "sparkwing runs cancel --run run-holder"
	var buf bytes.Buffer
	if err := renderQueue(&buf, qs, "pretty"); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "(stalled)") {
		t.Fatalf("stalled holder not marked in table:\n%s", out)
	}
	if !strings.Contains(out, "sparkwing runs cancel --run run-holder") {
		t.Fatalf("recovery command not shown:\n%s", out)
	}
	if strings.Contains(out, "box-slots") || strings.Contains(out, "--force") {
		t.Fatalf("stalled callout must not advertise a destructive verb:\n%s", out)
	}
}

func TestRenderQueue_PlainIsOneRecordPerLine(t *testing.T) {
	var buf bytes.Buffer
	if err := renderQueue(&buf, sampleQueueState(), "plain"); err != nil {
		t.Fatalf("render plain: %v", err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("want 3 resources + 1 holder + 1 waiter = 5 lines, got %d:\n%s", len(lines), buf.String())
	}
	if !strings.HasPrefix(lines[0], "resource\t") ||
		!strings.HasPrefix(lines[3], "holder\t") ||
		!strings.HasPrefix(lines[4], "waiter\t") {
		t.Fatalf("plain rows not tagged by kind:\n%s", buf.String())
	}
}

func TestRenderNoDaemon_CalmMessageAndEmptyJSON(t *testing.T) {
	var pretty bytes.Buffer
	if err := renderNoDaemon(&pretty, "pretty"); err != nil {
		t.Fatalf("pretty: %v", err)
	}
	if !strings.Contains(pretty.String(), "no admission daemon running") {
		t.Fatalf("missing calm message: %q", pretty.String())
	}

	var jsonBuf bytes.Buffer
	if err := renderNoDaemon(&jsonBuf, "json"); err != nil {
		t.Fatalf("json: %v", err)
	}
	var qs wingwire.QueueState
	if err := json.Unmarshal(jsonBuf.Bytes(), &qs); err != nil {
		t.Fatalf("no-daemon json must be well-formed: %v", err)
	}
	if len(qs.Holders) != 0 || len(qs.Waiters) != 0 {
		t.Fatalf("no-daemon queue should be empty: %+v", qs)
	}
}
