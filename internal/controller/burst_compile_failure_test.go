//go:build unix

package controller_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

// A Cloud job whose pipeline fails to compile has no attempt, so its compiler output reaches the requester only as
// the job's masked error in the run detail that `sparkwing runs status` reads.
func TestBurstCloudCompileFailureShowsMaskedCompilerOutput(t *testing.T) {
	modules, err := exec.CommandContext(t.Context(), "go", "env", "GOMODCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOMODCACHE", strings.TrimSpace(string(modules)))
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("GOPROXY", "off")
	t.Setenv("TOKEN", "ambient-requester-value-never-forward")
	const secret = "compile-output-secret-7f3a"
	repo, sha, engines, selected := burstSDKSourceFixture(t, "\nvar compileFailure int = \""+secret+"\"\n")
	_, plan, err := sdkserve.DefaultOperations.Plan(t.Context(), burstSDKPipelineName, map[string]string{}, nodeproto.RunContext{Pipeline: burstSDKPipelineName})
	if err != nil {
		t.Fatal(err)
	}
	observed := false
	proof := controller.BurstExecutionProof{
		Pipeline: burstSDKPipelineName, Plan: plan, Engine: selected.Identity, SecretArgs: map[string]string{"token": secret},
		Source: func(*testing.T) (string, string) { return repo, sha },
		Execute: func(options client.EngineOptions, award controllerwire.ClaimedUnit, _ controllerwire.Controller) {
			executeBurstCompileFailure(t, engines, selected, options, award, secret)
		},
		Failed: func(detail controllerwire.RunDetail) {
			observed = true
			if len(detail.Jobs) != 1 || len(detail.Attempts) != 0 {
				t.Fatalf("compile failure started an attempt: %+v", detail)
			}
			text := detail.Jobs[0].Error
			if !strings.Contains(text, "pipeline.go") || !strings.Contains(text, "cannot use") || strings.Contains(text, secret) || !strings.Contains(text, "***") {
				t.Fatalf("run detail lacks the masked compiler output: %q", text)
			}
		},
	}
	configureBurstCloudJob(t, &proof)
	controller.BurstExecutionAcceptance(t, "cloud", proof)
	if !observed {
		t.Fatal("acceptance did not read the failed run")
	}
}

func executeBurstCompileFailure(t *testing.T, engines *engine.Resolver, selected engine.Resolved, options client.EngineOptions, award controllerwire.ClaimedUnit, secret string) {
	t.Helper()
	ctx := t.Context()
	wire, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	private, err := client.NewNodeHostClient(options, award.RunID, award.JobID)
	if err != nil {
		t.Fatal(err)
	}
	defer private.Close()
	state, err := nodehost.NewRemoteHostStore(wire, private, award.UnitID, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := state.ReadSnapshot(ctx, award.RunID)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := source.ForExecution(*snapshot.Run.Spec)
	if err != nil {
		t.Fatal(err)
	}
	access, err := client.NewSourceAccess(options, award.RunID, spec)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(snapshot.Run.Spec)
	if err != nil {
		t.Fatal(err)
	}
	var plan nodeproto.PlanDoc
	if err := json.Unmarshal(snapshot.Run.Plan, &plan); err != nil {
		t.Fatal(err)
	}
	accepted := nodehost.AcceptedRun{RunID: award.RunID, Spec: raw, SpecHash: string(snapshot.Run.Spec.Hash), Plan: plan, PlanHash: snapshot.Run.PlanHash}
	row, err := state.ReadNode(ctx, runspec.NodeRef{RunID: runspec.RunID(award.RunID), NodeID: runspec.NodeID(award.JobID)})
	if err != nil {
		t.Fatal(err)
	}
	input, err := nodehost.NewNodeInput(accepted, row, selected, award.Runner)
	if err != nil {
		t.Fatal(err)
	}
	masker := secrets.NewMasker()
	masks := nodehost.NewMaskRegistry(masker)
	executor, err := nodehost.NewNodeExecutor(nodehost.ExecutorDependencies{
		Store: state, Masker: masker, Masks: masks,
		EnsureEngine: func(ctx context.Context, identity engineid.Identity, target releaseasset.Target) (engine.Resolved, error) {
			return engines.Ensure(ctx, identity, target)
		},
		Start: func(context.Context, nodehost.NodeInput, nodehost.ExecutorActivity) (nodehost.ExecutorProcess, error) {
			t.Error("a pipeline that failed to compile started a process")
			return nil, errors.New("unexpected start")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	host := &nodehost.ClaimedNodeHost{
		Store: state, Lease: lease.Keeper{Lease: time.Minute, Interval: 20 * time.Second},
		Admission: nodehost.ComposeStartGate(state, nil, false), Executor: executor, Masker: masker, Masks: masks,
		Source: source.Options{
			Workspace: t.TempDir(), Access: access, BundleTransport: options.HTTP.Transport,
			Engines: engines, Target: selected.Target, SDK: engineid.GoSDK,
			Base: procenv.Base{Ambient: os.Environ(), SubmitEnvAllow: []string{"GOCACHE", "GOMODCACHE"}, Masker: masker},
		},
	}
	if err := host.Execute(ctx, input); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("compile failure was not reported masked: %v", err)
	}
}
