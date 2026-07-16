package cluster

import (
	"reflect"
	"slices"
	"testing"
)

func TestTriggerRunnerArgsK8s(t *testing.T) {
	got := triggerRunnerArgs(TriggerLoopOptions{
		RunnerKind:    "k8s",
		K8sNamespace:  "sparkwing",
		K8sImage:      "example.com/sparkwing-runner:v1",
		K8sRunnerSA:   "runner-job",
		K8sPullSecret: "pull-secret",
		K8sCtrlURL:    "http://controller:4343",
		K8sLogsURL:    "http://logs:4344",
		Kubeconfig:    "/tmp/kubeconfig",
		ArtifactStore: "http://cache:4344",
	})
	want := []string{
		"--runner", "k8s",
		"--namespace", "sparkwing",
		"--image", "example.com/sparkwing-runner:v1",
		"--runner-sa", "runner-job",
		"--image-pull-secret", "pull-secret",
		"--runner-controller-url", "http://controller:4343",
		"--runner-logs-url", "http://logs:4344",
		"--kubeconfig", "/tmp/kubeconfig",
		"--artifact-store", "http://cache:4344",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("triggerRunnerArgs() = %#v, want %#v", got, want)
	}
}

func TestTriggerRunnerArgsDefaultInProcess(t *testing.T) {
	if got := triggerRunnerArgs(TriggerLoopOptions{}); len(got) != 0 {
		t.Fatalf("triggerRunnerArgs(default) = %#v, want empty", got)
	}
}

func TestHandleTriggerArgsPutFlagsBeforeTriggerID(t *testing.T) {
	got := handleTriggerArgs("trigger-1", TriggerLoopOptions{
		ControllerURL: "http://controller:4343",
		Token:         "token",
		RunnerKind:    "k8s",
		K8sNamespace:  "sparkwing",
		K8sImage:      "example.com/sparkwing-runner:v1",
	})
	triggerIdx := slices.Index(got, "trigger-1")
	runnerIdx := slices.Index(got, "--runner")
	if triggerIdx == -1 || runnerIdx == -1 {
		t.Fatalf("handleTriggerArgs() = %#v, want trigger id and --runner", got)
	}
	if runnerIdx > triggerIdx {
		t.Fatalf("handleTriggerArgs() = %#v, want flags before trigger id", got)
	}
	if got[len(got)-1] != "trigger-1" {
		t.Fatalf("handleTriggerArgs() last arg = %q, want trigger id", got[len(got)-1])
	}
}
