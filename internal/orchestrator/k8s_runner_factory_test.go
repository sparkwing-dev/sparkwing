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
