package charts

import (
	"strings"
	"testing"
)

func poolLauncherSettings() []string {
	return append(launcherImages(""),
		"controller.url=https://controller.example", "launcher.enabled=true", "launcher.tokenSecret.name=pool-token", "launcher.image.repository=registry.example/runner", "launcher.image.tag=test",
		"launcher.cpuCeiling=4", "launcher.memoryCeiling=8Gi")
}

func TestVerifiedPoolUsesLauncherWithoutPersistentRunner(t *testing.T) {
	settings := poolLauncherSettings()
	// Even an old persistent-cache setting must not retain cross-claim state.
	settings = append(settings, "runner.goCache.persistence.enabled=true")
	manifest := helmRenderAll(t, "./sparkwing-runner-bundle", "sparkwing", "pool", settings...)
	launcher, policies, customer := false, 0, false
	for _, r := range renderedResources(t, manifest) {
		if r.Kind == "PersistentVolumeClaim" {
			t.Fatal("verified pool retains a runner cache PVC")
		}
		if r.Kind == "Deployment" {
			if !strings.HasSuffix(r.Metadata.Name, "-launcher") {
				t.Fatalf("verified pool renders an in-pod runner: %s", r.Metadata.Name)
			}
			launcher = true
		}
		if strings.HasPrefix(r.Kind, "ValidatingAdmissionPolicy") {
			policies++
		}
		customer = customer || r.Kind == "ServiceAccount" && r.Metadata.Name == "sparkwing-customer-job"
	}
	if !launcher || policies != 4 || !customer || !strings.Contains(manifest, "name: \"pool-token\"") {
		t.Fatalf("pool must render launcher, pool credential, admission and customer identity: launcher=%v policies=%d customer=%v", launcher, policies, customer)
	}
}
