package wingd

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func writeAdmissionConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	indented := "admission:\n  " + strings.ReplaceAll(strings.TrimSuffix(body, "\n"), "\n", "\n  ") + "\n"
	if err := fssecure.WriteFile(path, []byte(indented)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_CONFIG", path)
	return path
}

func TestResolveAdmissionPolicyDefaultsToClassic(t *testing.T) {
	t.Setenv("SPARKWING_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
	policy, source, err := ResolveAdmissionPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if policy.Mode != admission.ModeClassic || source != "default" || policy.Scheduling.AgingEvery != 0 || policy.Scheduling.Burst.MaxCores != 0 || policy.Scheduling.BackfillDelay != nil {
		t.Fatalf("policy/source = %+v %q", policy, source)
	}
}

func TestResolveAdmissionPolicyAutoEnablesAdaptiveScheduling(t *testing.T) {
	writeAdmissionConfig(t, "mode: auto\n")
	policy, _, err := ResolveAdmissionPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if policy.Mode != admission.ModeAuto || policy.Scheduling.AgingEvery == 0 || policy.Scheduling.Burst.MaxCores == 0 {
		t.Fatalf("auto policy = %+v", policy)
	}
}

func TestResolveAdmissionPolicyCustomAndJevBounds(t *testing.T) {
	content := ("mode: jev\ncustom:\n  backfill_delay:\n    interactive: 750ms\n  class_weight:\n    interactive: 20\n  aging_every: 3\n  interactive_burst:\n    cores: 0.5\n    max_p99: 1500ms\n    min_samples: 5\njev:\n  timeout: 125ms\n  min_confidence: 0.8\n  min_probability: 0.75\n  max_backfill: 3s\n")
	writeAdmissionConfig(t, content)
	policy, _, err := ResolveAdmissionPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if policy.Mode != admission.ModeJev || policy.Jev.Timeout != 125*time.Millisecond || policy.Jev.MinConfidence != 0.8 || policy.Jev.MinProbability != 0.75 || policy.Jev.MaxBackfill != 3*time.Second {
		t.Fatalf("policy = %+v", policy)
	}
	if got := policy.Scheduling.BackfillDelay[admission.ClassInteractive]; got != 750*time.Millisecond {
		t.Fatalf("interactive delay = %s", got)
	}
	if policy.Scheduling.ClassWeight[admission.ClassInteractive] != 20 || policy.Scheduling.AgingEvery != 3 {
		t.Fatalf("custom scheduling = %+v", policy.Scheduling)
	}
	if policy.Scheduling.Burst.MaxCores != 0.5 || policy.Scheduling.Burst.MaxP99 != 1500*time.Millisecond || policy.Scheduling.Burst.MinSamples != 5 {
		t.Fatalf("interactive burst = %+v", policy.Scheduling.Burst)
	}
}

func TestInteractiveBurstEligibilityRequiresMeasuredShortClass(t *testing.T) {
	policy := AdmissionPolicy{Mode: admission.ModeAuto, Scheduling: admission.AutoPolicy()}
	resources := wingwire.HostResources{Cores: 1}
	request := &wingwire.AdmissionRequest{Class: "interactive", ExpectedP99MS: 1500, SampleCount: 3}
	if !policy.interactiveBurstEligible(request, resources) {
		t.Fatal("well-profiled interactive request was not burst eligible")
	}
	request.SampleCount = 2
	if policy.interactiveBurstEligible(request, resources) {
		t.Fatal("under-sampled request was burst eligible")
	}
	request.SampleCount = 3
	request.Class = "normal"
	if policy.interactiveBurstEligible(request, resources) {
		t.Fatal("normal request was burst eligible")
	}
}
