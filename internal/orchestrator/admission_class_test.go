package orchestrator

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestInferredAdmissionClass(t *testing.T) {
	tests := []struct {
		source string
		want   sparkwing.AdmissionClass
	}{
		{"manual", sparkwing.AdmissionNormal},
		{"pre-commit", sparkwing.AdmissionInteractive},
		{"pre-push@laptop", sparkwing.AdmissionInteractive},
		{"schedule", sparkwing.AdmissionBatch},
		{"post-commit", sparkwing.AdmissionBatch},
		{"retry", sparkwing.AdmissionBatch},
	}
	for _, test := range tests {
		if got := inferredAdmissionClass("", test.source); got != test.want {
			t.Errorf("source %q = %q, want %q", test.source, got, test.want)
		}
	}
	if got := inferredAdmissionClass(sparkwing.AdmissionCritical, "schedule"); got != sparkwing.AdmissionCritical {
		t.Fatalf("explicit class = %q", got)
	}
}
