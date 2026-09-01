package sparkwing

import (
	"os"
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

func TestGeneratedRequiresDocumentationStatesClaimBoundary(t *testing.T) {
	for _, path := range []string{"../docs/sdk-reference.md", "../pkg/docs/mirror/sdk-reference.md"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, receiver := range []string{"JobNode", "JobGroup"} {
			needle := "*" + receiver + ") Requires("
			found := false
			for _, line := range strings.Split(string(data), "\n") {
				if !strings.Contains(line, needle) {
					continue
				}
				found = true
				if !strings.Contains(strings.ToLower(line), "non-inline dispatched") {
					t.Errorf("%s %s.Requires summary omits the non-inline boundary", path, receiver)
				}
				break
			}
			if !found {
				t.Fatalf("%s has no generated %s.Requires entry", path, receiver)
			}
		}
	}
}
