package controller

import "testing"

func TestClassifyExecutionLocation(t *testing.T) {
	tests := []struct {
		name, principal, holder, runner string
		metered                         bool
		wantKind, wantName              string
	}{
		{"github", "github:42:koreyGambill/moonborn-ws", "runner:job-1", "actions-runner", false, "github-actions", "koreyGambill/moonborn-ws"},
		{"cloud", "agent:cloud", "pod:cloud-1", "cloud-1", true, "cloud", ""},
		{"cluster", "agent:cluster", "k8s-job:sw-abc", "warm-pool", false, "cluster", "warm-pool"},
		{"machine", "agent:moonborn", "runner:moonborn:1", "moonborn", false, "machine", "moonborn"},
		{"unknown", "", "", "", false, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, name := classifyExecutionLocation(tt.principal, tt.holder, tt.runner, tt.metered)
			if kind != tt.wantKind || name != tt.wantName {
				t.Fatalf("location = %q %q, want %q %q", kind, name, tt.wantKind, tt.wantName)
			}
		})
	}
}
