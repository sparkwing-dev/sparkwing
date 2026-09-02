package orchestrator

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildK8sRunnerFactoryRequiresAServiceAccount(t *testing.T) {
	tests := []struct {
		name           string
		serviceAccount string
		want           string
	}{
		{name: "empty is rejected", serviceAccount: "", want: "--runner-sa"},
		{name: "named account clears the check", serviceAccount: "runner-jobs", want: "kube config"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BuildK8sRunnerFactory(K8sRunnerFactoryConfig{
				Kubeconfig:     filepath.Join(t.TempDir(), "missing.kubeconfig"),
				Namespace:      "builds",
				Image:          "img",
				ControllerURL:  "http://controller",
				ServiceAccount: tc.serviceAccount,
			})
			if err == nil {
				t.Fatal("BuildK8sRunnerFactory succeeded without a usable kubeconfig")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

func TestBuildK8sRunnerFactoryRefusesAnUnparseableCeiling(t *testing.T) {
	tests := []struct {
		name   string
		cpu    string
		memory string
		want   string
	}{
		{name: "cpu is not a quantity", cpu: "2 cores", want: "--k8s-cpu-ceiling"},
		{name: "memory is not a quantity", memory: "2 GB", want: "--k8s-memory-ceiling"},
		{name: "cpu is not positive", cpu: "0m", want: "--k8s-cpu-ceiling"},
		{name: "memory is negative", memory: "-1Gi", want: "--k8s-memory-ceiling"},
		{name: "both parse", cpu: "8", memory: "16Gi", want: "kube config"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BuildK8sRunnerFactory(K8sRunnerFactoryConfig{
				Kubeconfig:     filepath.Join(t.TempDir(), "missing.kubeconfig"),
				Namespace:      "builds",
				Image:          "img",
				ControllerURL:  "http://controller",
				ServiceAccount: "runner-jobs",
				CPUCeiling:     tc.cpu,
				MemoryCeiling:  tc.memory,
			})
			if err == nil {
				t.Fatal("BuildK8sRunnerFactory succeeded, want a startup refusal")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}
