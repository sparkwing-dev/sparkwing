package sparkwing

import (
	"context"
	"os"
	"testing"

	nodeproto "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

type branchReadingPipeline struct {
	Base
	seen *[]*Git
}

func (p branchReadingPipeline) Plan(_ context.Context, plan *Plan, _ NoInputs, run RunContext) error {
	*p.seen = append(*p.seen, run.Git)
	branch := run.Git.Branch
	Job(plan, "deploy", func(context.Context) error { return nil }).SkipIf(func(context.Context) bool { return branch != "main" })
	return nil
}

func TestPlanReadsGitDuringDiscoveryAndWithoutGitFacts(t *testing.T) {
	isolateRegistry(t)
	var seen []*Git
	RegisterEntrypoint("Deploy", func() Pipeline[NoInputs] { return branchReadingPipeline{seen: &seen} })
	workDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	doc, err := (sdkOperations{}).Describe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Pipelines) != 1 || doc.Pipelines[0].Error != nil {
		t.Fatalf("discovery failed: %+v", doc.Pipelines)
	}
	if _, _, err := (sdkOperations{}).Plan(t.Context(), "Deploy", nil, nodeproto.RunContext{RunID: "run-1", Pipeline: "deploy"}); err != nil {
		t.Fatalf("Plan without git facts: %v", err)
	}
	if _, _, err := (sdkOperations{}).Plan(t.Context(), "Deploy", nil, nodeproto.RunContext{RunID: "run-2", Pipeline: "deploy", Git: &nodeproto.Git{SHA: "abc", Branch: "main"}}); err != nil {
		t.Fatalf("Plan with git facts: %v", err)
	}
	if len(seen) < 3 {
		t.Fatalf("Plan ran %d times, want at least 3", len(seen))
	}
	for i, git := range seen[:len(seen)-1] {
		if git == nil || git.Branch != "" || git.SHA != "" || git.WorkDir() != workDir {
			t.Errorf("planning call %d saw Git %+v, want empty data fields rooted at %s", i, git, workDir)
		}
	}
	if last := seen[len(seen)-1]; last.Branch != "main" || last.SHA != "abc" {
		t.Errorf("Plan with git facts saw %+v", last)
	}
}
