package wingd

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestResolveAdmissionPolicyDefaultsToClassic(t *testing.T) {
	policy, source, err := ResolveAdmissionPolicy(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if policy.Mode != admission.ModeClassic || source != "default" || policy.Scheduling.AgingEvery != 0 || policy.Scheduling.Burst.MaxCores != 0 || policy.Scheduling.BackfillDelay != nil {
		t.Fatalf("policy/source = %+v %q", policy, source)
	}
}

func TestResolveAdmissionPolicyAutoEnablesAdaptiveScheduling(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admission.yaml")
	if err := os.WriteFile(path, []byte("mode: auto\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	policy, _, err := ResolveAdmissionPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Mode != admission.ModeAuto || policy.Scheduling.AgingEvery == 0 || policy.Scheduling.Burst.MaxCores == 0 {
		t.Fatalf("auto policy = %+v", policy)
	}
}

func TestResolveAdmissionPolicyCustomAndJevBounds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admission.yaml")
	content := []byte("mode: jev\ncustom:\n  backfill_delay:\n    interactive: 750ms\n  class_weight:\n    interactive: 20\n  aging_every: 3\n  interactive_burst:\n    cores: 0.5\n    max_p99: 1500ms\n    min_samples: 5\njev:\n  timeout: 125ms\n  min_confidence: 0.8\n  min_probability: 0.75\n  max_backfill: 3s\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	policy, _, err := ResolveAdmissionPolicy(path)
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
