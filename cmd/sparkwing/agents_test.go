package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestEnrollAgentSendsExactOperatorEnvelope(t *testing.T) {
	want := agentEnrollment{
		TokenPrefix: "swr_01234567", Kind: "gateway", Location: "cloud",
		Capabilities: []string{"linux-amd64", "gpu"}, BasePriority: 20,
		PriorityCeiling: 40, MaxConcurrent: 3,
		Budget: agentResources{Cores: 6, MemoryBytes: 12 << 30},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/v1/agents/build-gateway" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer admin-secret" {
			t.Errorf("Authorization = %q", got)
		}
		var got agentEnrollment
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode enrollment: %v", err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("enrollment = %+v, want %+v", got, want)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	if err := enrollAgent(context.Background(), server.URL, "admin-secret", "build-gateway", want); err != nil {
		t.Fatalf("enrollAgent: %v", err)
	}
}

func TestEnrollAgentErrorNeverEchoesCredentialPrefix(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "enrollment refused", http.StatusConflict)
	}))
	defer server.Close()

	const prefix = "swr_01234567"
	err := enrollAgent(context.Background(), server.URL, "admin-secret", "desk",
		agentEnrollment{TokenPrefix: prefix})
	if err == nil {
		t.Fatal("enrollment failure returned nil")
	}
	if strings.Contains(err.Error(), prefix) {
		t.Fatalf("error exposed credential prefix: %v", err)
	}
}

func TestAgentActiveSlotsUsesClaimCountWhenKnown(t *testing.T) {
	slots := 3
	if got := agentActiveSlots(agentView{ActiveJobs: []string{"run-a"}, ActiveSlots: &slots}); got != 3 {
		t.Fatalf("registered active slots = %d, want 3", got)
	}
	if got := agentActiveSlots(agentView{ActiveJobs: []string{"run-a", "run-b"}}); got != 2 {
		t.Fatalf("legacy active slots fallback = %d, want 2", got)
	}
}
