package orchestrator

import (
	"os"
	"reflect"
	"testing"
)

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

func TestPodRunnerInfo_TypeStaysUnclassifiedWhenUnset(t *testing.T) {
	t.Setenv("SPARKWING_RUNNER_NAME", "static-mac")
	os.Unsetenv("SPARKWING_RUNNER_TYPE")
	os.Unsetenv("SPARKWING_RUNNER_LABELS")
	info := podRunnerInfo()
	if info == nil || info.Name != "static-mac" || info.Type != "" {
		t.Errorf("info = %+v, want the name and no type", info)
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
