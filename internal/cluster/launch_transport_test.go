package cluster

import (
	"context"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
)

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
