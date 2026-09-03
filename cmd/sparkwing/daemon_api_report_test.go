package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
)

func ptr[T any](v T) *T { return &v }

func TestDaemonStatusJSONCarriesAPIReadyWhenFalse(t *testing.T) {
	report := daemonReport{
		Running: true, Healthy: false, BinaryVersion: "v1",
		Socket: "/tmp/d.sock", APISocket: "/tmp/api.sock",
		APIReady: ptr(false), APIError: "listen /tmp/api.sock: permission denied",
	}
	out := captureStdout(t, func() {
		if err := emitDaemonReport(report, "json"); err != nil {
			t.Fatalf("emit: %v", err)
		}
	})
	var decoded map[string]any
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("decode %s: %v", out, err)
	}
	ready, present := decoded["api_ready"]
	if !present {
		t.Fatalf("api_ready is absent from %s; a consumer cannot tell not-served from not-reported", out)
	}
	if ready != false {
		t.Fatalf("api_ready = %v, want false", ready)
	}
	if decoded["api_error"] != report.APIError {
		t.Fatalf("api_error = %v, want %q", decoded["api_error"], report.APIError)
	}
}

func TestDaemonStatusJSONOmitsAPIReadyWhenTheDaemonDoesNotReportIt(t *testing.T) {
	report := daemonReport{Running: true, Healthy: true, BinaryVersion: "v1", APISocket: "/tmp/api.sock"}
	out := captureStdout(t, func() {
		if err := emitDaemonReport(report, "json"); err != nil {
			t.Fatalf("emit: %v", err)
		}
	})
	var decoded map[string]any
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("decode %s: %v", out, err)
	}
	if _, present := decoded["api_ready"]; present {
		t.Fatalf("api_ready is present in %s for a daemon that does not report it", out)
	}
}

func TestDaemonStatusPrettyNamesTheAPIFault(t *testing.T) {
	cases := []struct {
		name   string
		report daemonReport
		want   string
		absent string
	}{
		{
			name: "serving",
			report: daemonReport{
				Running: true, BinaryVersion: "v1", APISocket: "/tmp/api.sock", APIReady: ptr(true),
			},
			want: "controller API on /tmp/api.sock",
		},
		{
			name: "unbound",
			report: daemonReport{
				Running: true, BinaryVersion: "v1", APISocket: "/tmp/api.sock",
				APIReady: ptr(false), APIError: "permission denied",
			},
			want: "controller API not served: permission denied",
		},
		{
			name: "not reported",
			report: daemonReport{
				Running: true, BinaryVersion: "v1", APISocket: "/tmp/api.sock",
			},
			absent: "controller API",
		},
		{
			name: "no artifact routes",
			report: daemonReport{
				Running: true, BinaryVersion: "v1", APISocket: "/tmp/api.sock", APIReady: ptr(true),
				ArtifactStoreError: "unsupported cache scheme",
			},
			want: "no artifact routes: unsupported cache scheme",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := captureStdout(t, func() {
				if err := emitDaemonReport(tc.report, "pretty"); err != nil {
					t.Fatalf("emit: %v", err)
				}
			})
			if tc.want != "" && !strings.Contains(out, tc.want) {
				t.Fatalf("output %q does not name %q", out, tc.want)
			}
			if tc.absent != "" && strings.Contains(out, tc.absent) {
				t.Fatalf("output %q names %q for a daemon that does not report it", out, tc.absent)
			}
		})
	}
}

func TestDaemonWithAnUnboundAPISocketIsNotHealthy(t *testing.T) {
	unbound := daemonReport{Running: true, APIReady: ptr(false)}
	if !daemonAPIUnusable(unbound) {
		t.Fatal("a serving daemon with an unbound API socket passes as usable")
	}
	draining := daemonReport{Running: true, Draining: true, APIReady: ptr(false)}
	if daemonAPIUnusable(draining) {
		t.Fatal("a draining daemon is reported as an API fault; closing the socket is how a drain works")
	}
	old := daemonReport{Running: true}
	if daemonAPIUnusable(old) {
		t.Fatal("a daemon that does not report api_ready is judged on it")
	}
}

func TestTheInstalledDaemonVerbResolvesAnArtifactStore(t *testing.T) {
	t.Setenv("SPARKWING_CACHE_URL", "bogus-scheme://nowhere")
	art, fault := orchestrator.WingdArtifactStore(context.Background())
	if art != nil {
		t.Fatalf("a cache URL that will not open resolved to %T", art)
	}
	if fault == "" {
		t.Fatal("a cache URL that will not open reported no fault, so the daemon would serve artifact routes it cannot back")
	}
	if !strings.Contains(runWingdRunSource(t), "WingdArtifactStore") {
		t.Fatal("the installed sparkwing's wingd run verb does not resolve an artifact store, so its daemon serves none")
	}
}

func runWingdRunSource(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile("wingd.go")
	if err != nil {
		t.Fatalf("read the wingd verb: %v", err)
	}
	return string(src)
}
