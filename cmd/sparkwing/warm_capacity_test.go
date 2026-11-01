package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestWarmCapacityCommandsUseControllerTransport(t *testing.T) {
	policy := controllerwire.WarmCapacitySettings{Class: "sparkwing-2cpu", Mode: "off", StartTimeTargetSeconds: 10}
	count := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		if r.URL.Path != "/api/v1/operator/warm-capacity" || r.Header.Get("Authorization") != "Bearer deployment-secret" {
			t.Errorf("unexpected request %s %v", r.URL, r.Header)
		}
		if r.Method == http.MethodPut {
			if err := json.NewDecoder(r.Body).Decode(&policy); err != nil {
				t.Error(err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(controllerwire.WarmCapacity{Classes: []controllerwire.WarmCapacityClass{{WarmCapacitySettings: policy, Desired: policy.ManualCount}}, NewSignups24h: 2, ActiveTeams7d: 3}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	manager, err := client.NewOperatorManager(client.EngineOptions{URL: server.URL, Bearer: func(context.Context) (string, error) { return "deployment-secret", nil }})
	if err != nil {
		t.Fatal(err)
	}
	closes := 0
	connect := func(_ context.Context, profile string) (controllerwire.OperatorManager, func(), error) {
		if profile != "deployment" {
			t.Errorf("profile %q", profile)
		}
		return manager, func() { closes++ }, nil
	}
	run := func(args ...string) string {
		t.Helper()
		args = append([]string{"operator", "warm-capacity"}, args...)
		inv, err := clisurface.Parse("sparkwing", args)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := runWarmCapacityCommand(t.Context(), inv, &out, connect); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	shown := run("show", "--profile", "deployment", "-o", "json")
	if !strings.Contains(shown, `"p50_seconds":null`) || !strings.Contains(shown, `"mode":"off"`) {
		t.Fatalf("projection %s", shown)
	}
	shown = run("set", "--class", "sparkwing-2cpu", "--mode", "manual", "--count", "2", "--start-time-target", "500ms", "--profile", "deployment", "-o", "plain")
	for _, value := range []string{"sparkwing-2cpu", "manual", "0.5s", "COLD 24h/7d", "UPSIZES 24h/7d", "New signups", "Active teams", "-/-"} {
		if !strings.Contains(shown, value) {
			t.Fatalf("missing %q in %s", value, shown)
		}
	}
	if policy.ManualCount != 2 || policy.StartTimeTargetSeconds != .5 || count != 2 || closes != 2 {
		t.Fatalf("policy=%+v requests=%d closes=%d", policy, count, closes)
	}
	run("set", "--class", "sparkwing-2cpu", "--mode", "auto", "--min", "1", "--max", "5", "--profile", "deployment", "-o", "json")
	if policy.AutoMin != 1 || policy.AutoMax != 5 || policy.StartTimeTargetSeconds != 10 {
		t.Fatalf("auto=%+v", policy)
	}
}

func TestWarmCapacityInvalidInputNeverConnects(t *testing.T) {
	for _, flags := range [][]string{
		{"--class", "bad", "--mode", "off"},
		{"--class", "sparkwing-2cpu", "--mode", "bad"},
		{"--class", "sparkwing-2cpu", "--mode", "auto", "--min", "2", "--max", "1"},
		{"--class", "sparkwing-2cpu", "--mode", "manual", "--count", "-1"},
		{"--class", "sparkwing-2cpu", "--mode", "off", "--start-time-target", "0s"},
	} {
		inv, err := clisurface.Parse("sparkwing", append([]string{"operator", "warm-capacity", "set"}, flags...))
		if err != nil {
			continue
		}
		var out bytes.Buffer
		if err := runWarmCapacityCommand(t.Context(), inv, &out, nil); err == nil || exitCodeFor(err) != 2 || out.Len() != 0 {
			t.Fatalf("accepted %v: %v %s", flags, err, &out)
		}
	}
}
