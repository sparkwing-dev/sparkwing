package cluster

import (
	"context"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
)

// A typo such as http://api.sparkwing.dev must not send a runner's machine
// token in clear text; in-cluster and loopback controllers stay on http.
func TestControllerTransportRefusesPlainHTTPOffTheCluster(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://api.sparkwing.dev":                               true,
		"http://127.0.0.1:4344":                                   true,
		"http://[::1]:4344":                                       true,
		"http://localhost:4344":                                   true,
		"http://sparkwing-controller.sparkwing.svc":               true,
		"http://sparkwing-controller.sparkwing.svc.cluster.local": true,
		"http://api.sparkwing.dev":                                false,
		"http://10.0.0.5:4344":                                    false,
		"http://controller.svc.example.com":                       false,
		"http://localhost.example.com":                            false,
	} {
		if err := checkControllerTransport("--controller", raw); (err == nil) != ok {
			t.Errorf("%s: err = %v, want allowed %v", raw, err, ok)
		}
	}
}

func TestLauncherCommandRefusesPlainHTTPController(t *testing.T) {
	image := "example.com/image@sha256:" + strings.Repeat("a", 64)
	err := RunLaunchCLI(t.Context(), []string{"--controller=http://api.sparkwing.dev", "--image=" + image, "--docker-image=" + image, "--cpu-ceiling=1", "--memory-ceiling=1Gi", "--credentials-dir=" + t.TempDir()}, func(context.Context, launcher.CloudJobConfig, func(context.Context) (string, error)) (*launcher.Launcher, error) {
		t.Error("plain http controller reached the runtime factory")
		return nil, nil
	})
	if err == nil || !strings.Contains(err.Error(), "unencrypted") {
		t.Fatalf("launcher with http://api.sparkwing.dev = %v, want a refusal", err)
	}
}
