package orchestrator

import (
	"context"
	"os"
	"reflect"
	"slices"
	"sort"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
)

func TestRunnerInfoFor_NilRunnerIsLocalDefault(t *testing.T) {
	info := runnerInfoFor(nil)
	if info.Type != "local" || info.Name != "local" {
		t.Errorf("nil runner: got %+v, want type/name = local", info)
	}
	labels := append([]string(nil), info.Labels...)
	sort.Strings(labels)
	if !slices.Contains(labels, "local") {
		t.Errorf("nil runner labels = %v, want at least [local]", labels)
	}
}

func TestRunnerInfoFor_InProcessAdvertisesLocal(t *testing.T) {
	r := NewInProcessRunner(Backends{})
	info := runnerInfoFor(r)
	if info.Type != "local" || info.Name != "local" {
		t.Errorf("got %+v, want type/name = local", info)
	}
	if !info.HasLabel("local") {
		t.Errorf("info.HasLabel(local) = false; labels = %v", info.Labels)
	}
}

func TestRunnerInfoFor_KubernetesLabelClassifies(t *testing.T) {
	r := &fakeAdvRunner{labels: []string{"kubernetes", "os=linux", "cloud-linux"}}
	info := runnerInfoFor(r)
	if info.Type != "kubernetes" {
		t.Errorf("type = %q, want kubernetes", info.Type)
	}
	if !reflect.DeepEqual(info.Labels, []string{"kubernetes", "os=linux", "cloud-linux"}) {
		t.Errorf("labels = %v", info.Labels)
	}
}

func TestPodRunnerInfo_FromEnv(t *testing.T) {
	t.Setenv("SPARKWING_RUNNER_NAME", "warm-pool-a")
	t.Setenv("SPARKWING_RUNNER_TYPE", "kubernetes")
	t.Setenv("SPARKWING_RUNNER_LABELS", "kubernetes, cloud-linux ,os=linux")
	info := podRunnerInfo()
	if info == nil {
		t.Fatal("nil info")
	}
	if info.Name != "warm-pool-a" || info.Type != "kubernetes" {
		t.Errorf("identity wrong: %+v", info)
	}
	if !reflect.DeepEqual(info.Labels, []string{"kubernetes", "cloud-linux", "os=linux"}) {
		t.Errorf("labels = %v", info.Labels)
	}
}

func TestPodRunnerInfo_TypeDefaultsToKubernetes(t *testing.T) {
	t.Setenv("SPARKWING_RUNNER_NAME", "static-mac")
	os.Unsetenv("SPARKWING_RUNNER_TYPE")
	os.Unsetenv("SPARKWING_RUNNER_LABELS")
	info := podRunnerInfo()
	if info == nil || info.Type != "kubernetes" {
		t.Errorf("type = %v, want kubernetes default", info)
	}
}

func TestPodRunnerInfo_EmptyEnvReturnsNil(t *testing.T) {
	os.Unsetenv("SPARKWING_RUNNER_NAME")
	os.Unsetenv("SPARKWING_RUNNER_TYPE")
	os.Unsetenv("SPARKWING_RUNNER_LABELS")
	if got := podRunnerInfo(); got != nil {
		t.Errorf("got %+v, want nil", got)
	}
}

// fakeAdvRunner satisfies runner.Runner + runner.LabelAdvertiser
// for the label-classification test.
type fakeAdvRunner struct {
	labels []string
}

func (f *fakeAdvRunner) RunNode(_ context.Context, _ runner.Request) runner.Result {
	return runner.Result{}
}
func (f *fakeAdvRunner) AdvertisedLabels() []string { return f.labels }
