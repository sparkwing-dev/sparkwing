package store

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestDeclaredPipelineReferenceProperty(t *testing.T) {
	s, demand := unitClaimFixture(t)
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	random := rand.New(rand.NewPCG(91, 310))
	var pipeline string
	for range 64 {
		pipeline = fmt.Sprintf("producer-%x", random.Uint64())
		spec, err := json.Marshal(nodeproto.JobSpec{ID: "job", References: []nodeproto.PipelineReference{{Pipeline: pipeline, Job: "output"}}})
		if err != nil {
			t.Fatal(err)
		}
		rewriteSchemaExec(t, s.db, `UPDATE nodes SET spec_json=? WHERE team='a' AND run_id='run' AND node_id='job'`, spec)
		target := capability.Target{Team: "a", Reference: &capability.PipelineReferenceTarget{Pipeline: pipeline}}
		if err := s.AsOperator().CheckBinding(t.Context(), p, capability.BindReadSet, target); err != nil {
			t.Fatalf("declared reference refused: %v", err)
		}
		for _, mutation := range []string{"undeclared", "team", "run", "node", "generation"} {
			forged := target
			ref := *target.Reference
			forged.Reference = &ref
			switch mutation {
			case "undeclared":
				forged.Reference.Pipeline += "-other"
			case "team":
				forged.Team = "b"
			case "run":
				forged.RunID = "run"
			case "node":
				forged.NodeID = "job"
			case "generation":
				forged.SourceGeneration = 1
			}
			if err := s.AsOperator().CheckBinding(t.Context(), p, capability.BindReadSet, forged); err == nil {
				t.Fatalf("%s widened the reference", mutation)
			}
		}
	}
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET generation=generation+1 WHERE team='a' AND run_id='run' AND node_id='job'`)
	if err := s.AsOperator().CheckBinding(t.Context(), p, capability.BindReadSet, capability.Target{Team: "a", Reference: &capability.PipelineReferenceTarget{Pipeline: pipeline}}); err == nil {
		t.Fatal("replacement job retained old reference authority")
	}
}

func TestPipelineReferenceOptOutMustBeDeclared(t *testing.T) {
	s, demand := unitClaimFixture(t)
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	for _, declared := range []bool{false, true} {
		spec, err := json.Marshal(nodeproto.JobSpec{ID: "job", References: []nodeproto.PipelineReference{{Pipeline: "producer", Job: "output", FromAnyRun: declared}}})
		if err != nil {
			t.Fatal(err)
		}
		rewriteSchemaExec(t, s.db, `UPDATE nodes SET spec_json=? WHERE team='a' AND run_id='run' AND node_id='job'`, spec)
		for _, requested := range []bool{false, true} {
			target := capability.Target{Team: "a", Reference: &capability.PipelineReferenceTarget{Pipeline: "producer", JobID: "output", FromAnyRun: requested}}
			err := s.AsOperator().CheckBinding(t.Context(), principal, capability.BindReadSet, target)
			if (err == nil) != (!requested || declared) {
				t.Fatalf("declared=%v requested=%v: %v", declared, requested, err)
			}
		}
	}
}

func TestPipelineOutputRequiresVerifiedDefaultBranchOrDeclaredOptOut(t *testing.T) {
	s, demand := unitClaimFixture(t)
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,finished_at,log_retention_class,log_retention_days) VALUES ('a','producer-run',2,'producer','success',?,'producer',1,'gh:1','host','execute','account',1,2,'r30',30)`, []byte(`{"version":1,"request":{"pipeline":"producer"}}`))
	rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,outcome,finished_at,spec_json,execution_json) VALUES ('a','producer-run','output','done','success',2,?,?)`, []byte(`{"id":"output"}`), []byte(`{}`))
	for _, declared := range []bool{false, true} {
		spec, err := json.Marshal(nodeproto.JobSpec{ID: "job", References: []nodeproto.PipelineReference{{Pipeline: "producer", Job: "output", FromAnyRun: declared}}})
		if err != nil {
			t.Fatal(err)
		}
		rewriteSchemaExec(t, s.db, `UPDATE nodes SET spec_json=? WHERE team='a' AND run_id='run' AND node_id='job'`, spec)
		err = s.AsOperator().CheckBinding(t.Context(), principal, capability.BindReadSet, capability.Target{Team: "a", RunID: "producer-run", NodeID: "output"})
		if (err == nil) != declared {
			t.Fatalf("direct unverified output, opt-out=%v: %v", declared, err)
		}
	}
}

func TestProvenPipelineOutputNeedsADeclaredProvenReferenceAndACleanRun(t *testing.T) {
	s, demand := unitClaimFixture(t)
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	tenant := &Tenant{s: s, team: "a"}
	reader, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	proof, err := assertedProvenance(t, "ci", "refs/heads/main", provenSHA).FetchedFrom(1, "acme/repo", "refs/heads/main", provenSHA)
	if err != nil {
		t.Fatal(err)
	}
	reader.Request.Request.Git.SHA, reader.Request.Request.Git.Repository.Key = provenSHA, provenRepo
	rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json=?,git_sha=?,repo_key=? WHERE team='a' AND id='run'`, sealWith(t, reader.Request, proof), provenSHA, string(provenRepo))
	candidate, err := assertedProvenance(t, "producer", "refs/heads/agent", provenSHA).FetchedFrom(1, "acme/repo", "refs/heads/agent", provenSHA)
	if err != nil {
		t.Fatal(err)
	}
	read := capability.Target{Team: "a", RunID: "producer-run", NodeID: "output"}
	for _, tc := range []struct {
		name              string
		anyRun, provenRun bool
		options           runspec.Options
		want              bool
	}{
		{name: "proven reference, clean run", provenRun: true, want: true},
		{name: "proven reference, start-at run", provenRun: true, options: runspec.Options{StartAt: runspec.Setting[string]{Optional: runspec.Optional[string]{Set: true, Value: "sdk-typescript"}}}},
		{name: "default reference, clean run"},
		{name: "any-run reference, start-at run", anyRun: true, options: runspec.Options{StartAt: runspec.Setting[string]{Optional: runspec.Optional[string]{Set: true, Value: "sdk-typescript"}}}, want: true},
	} {
		rewriteSchemaExec(t, s.db, `DELETE FROM nodes WHERE team='a' AND run_id='producer-run'`)
		rewriteSchemaExec(t, s.db, `DELETE FROM runs WHERE team='a' AND id='producer-run'`)
		if err := tenant.TestOnlySeedSealedRun(t.Context(), "producer-run", "producer", provenRepo, provenSHA, candidate, tc.options, time.Unix(1700000000, 0)); err != nil {
			t.Fatal(err)
		}
		rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,outcome,finished_at,spec_json,execution_json) VALUES ('a','producer-run','output','done','success',2,?,?)`, []byte(`{"id":"output"}`), []byte(`{}`))
		spec, err := json.Marshal(nodeproto.JobSpec{ID: "job", References: []nodeproto.PipelineReference{{Pipeline: "producer", Job: "output", FromAnyRun: tc.anyRun, FromProvenRun: tc.provenRun}}})
		if err != nil {
			t.Fatal(err)
		}
		rewriteSchemaExec(t, s.db, `UPDATE nodes SET spec_json=? WHERE team='a' AND run_id='run' AND node_id='job'`, spec)
		if err := s.AsOperator().CheckBinding(t.Context(), principal, capability.BindReadSet, read); (err == nil) != tc.want {
			t.Fatalf("%s: %v", tc.name, err)
		}
	}
}
