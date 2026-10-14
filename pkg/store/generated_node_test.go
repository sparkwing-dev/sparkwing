package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// A generated node's recorded spec is admitted beside its run's plan: its
// pipeline references resolve under its claim as a planned node's do, while a
// pipeline it does not name and a secret the pipeline never declared stay
// refused.
func TestAGeneratedNodesRecordedSpecIsAdmitted(t *testing.T) {
	ctx, now := context.Background(), time.Now()
	s := storetest.Open(t)
	if err := s.CreateRun(ctx, store.Run{ID: "upstream", Pipeline: "build", Status: "success", StartedAt: now, FinishedAt: &now}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNode(ctx, store.Node{RunID: "upstream", NodeID: "build", Status: "done", Outcome: "success"}); err != nil {
		t.Fatal(err)
	}
	plan := []byte(`{"nodes":[{"id":"targets","deps":[],"closures":{"generator":["targets/generator/0"]}}]}`)
	if err := s.CreateRun(ctx, store.Run{ID: "hosted", Pipeline: "deploy", Status: "running", StartedAt: now, PlanSnapshot: plan}); err != nil {
		t.Fatal(err)
	}
	gen := &store.GeneratedNode{
		Source: "targets", Generator: "targets/generator/0", Batch: 1,
		Spec: []byte(`{"id":"generated","deps":["targets"],"pipeline_refs":[{"pipeline":"build","node":"build"}],"spec_hash":"sha256:g"}`),
	}
	if err := s.CreateNode(ctx, store.Node{RunID: "hosted", NodeID: "generated", Status: "pending", Deps: []string{"targets"}, SpecHash: "sha256:g", Generated: gen}); err != nil {
		t.Fatal(err)
	}
	if row, err := s.GetNode(ctx, "hosted", "generated"); err != nil || row.Generated == nil || row.Generated.Source != "targets" || string(row.Generated.Spec) != string(gen.Spec) {
		t.Fatalf("generated row = %+v, %v; want its recorded spec back", row, err)
	}
	tok := store.ClaimToken{Team: store.DefaultTeam, Kind: store.ClaimTokenNode, RunID: "hosted", NodeID: "generated"}
	in, err := s.ResolveClaimInput(ctx, tok, store.ClaimInputRequest{Kind: store.ClaimInputLastRun, Pipeline: "build", Node: "build"}, now)
	if err != nil || in.RunID != "upstream" {
		t.Fatalf("declared reference = %+v, %v; want upstream's build", in, err)
	}
	_, err = s.ResolveClaimInput(ctx, tok, store.ClaimInputRequest{Kind: store.ClaimInputLastRun, Pipeline: "private", Node: "secret"}, now)
	if !errors.Is(err, store.ErrInputUndeclared) {
		t.Fatalf("undeclared reference = %v, want ErrInputUndeclared", err)
	}
	tenant, err := s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.ReleaseClaimSecret(ctx, tok, "EXTRA_GENERATED_SECRET", now); !errors.Is(err, store.ErrSecretUndeclared) {
		t.Fatalf("undeclared secret = %v, want ErrSecretUndeclared", err)
	}
}
