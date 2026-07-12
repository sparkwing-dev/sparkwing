package orchestrator

import (
	"strings"
	"testing"
)

func TestUnlocatableChildError_NamesRealCauseNotPhantomVerb(t *testing.T) {
	msg := unlocatableChildError("light").Error()

	if strings.Contains(msg, "pipeline add") {
		t.Fatalf("error recommends the phantom `sparkwing pipeline add` verb: %q", msg)
	}
	for _, want := range []string{
		"light",
		"no git identity",
		"sparkwing configure xrepo add",
		"WithFreshRepo",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error missing %q; got %q", want, msg)
		}
	}
}

func TestRepoDeclaresPipeline_FalseWithoutSparkwingDir(t *testing.T) {
	if repoDeclaresPipeline(t.TempDir(), "anything") {
		t.Fatal("a directory with no .sparkwing/ must not claim to declare a pipeline")
	}
}
