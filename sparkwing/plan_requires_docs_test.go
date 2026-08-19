package sparkwing

import (
	"strings"
	"testing"
)

func TestRequiresDocumentationStatesDispatchBehavior(t *testing.T) {
	tests := []struct {
		file     string
		receiver string
	}{
		{file: "plan.go", receiver: "JobNode"},
		{file: "combinator.go", receiver: "JobGroup"},
	}
	for _, tt := range tests {
		t.Run(tt.receiver, func(t *testing.T) {
			doc := strings.Join(strings.Fields(methodDoc(t, tt.file, tt.receiver, "Requires")), " ")
			for _, want := range []string{"dispatched", "queue_timeout", "direct runs", "inline"} {
				if !strings.Contains(doc, want) {
					t.Errorf("Requires documentation does not contain %q", want)
				}
			}
			if strings.Contains(doc, "fails the run at validation") {
				t.Fatal("Requires documentation claims unsatisfied labels fail at validation")
			}
		})
	}
}

func TestRequiresStorageDocumentationStatesClaimBoundary(t *testing.T) {
	doc := strings.Join(strings.Fields(jobNodeFieldDoc(t, "requires")), " ")
	for _, want := range []string{"dispatched", "claim"} {
		if !strings.Contains(doc, want) {
			t.Errorf("requires storage documentation does not contain %q", want)
		}
	}
	if strings.Contains(doc, "restricts the job") {
		t.Fatal("requires storage documentation claims a universal runner restriction")
	}
}
