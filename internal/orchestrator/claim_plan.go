package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func runPlanCLI(args []string) error {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "write the run's plan document to stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*asJSON {
		return errors.New("plan writes only --json")
	}
	runID := os.Getenv("SPARKWING_RUN_ID")
	if runID == "" {
		return errors.New("SPARKWING_RUN_ID names no run")
	}
	// safety: Plan is the team's code, and anything it prints to stdout would
	// corrupt the document, so it prints to stderr instead.
	out := os.Stdout
	os.Stdout = os.Stderr
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	c := client.NewWithToken(os.Getenv("SPARKWING_CONTROLLER_URL"), nil, takeAgentToken())
	run, err := c.GetRunForExecution(ctx, runID)
	if err != nil {
		return fmt.Errorf("read run %s: %w", runID, err)
	}
	reg, ok := sparkwing.Lookup(run.Pipeline)
	if !ok {
		return unknownPipelineErr(run.Pipeline)
	}
	rc := runContextFor(run)
	sparkwing.SetGit(rc.Git)
	plan, err := reg.Invoke(sparkwingruntime.WithLogger(ctx, NewJSONRenderer()), checkoutInvokeArgs(run.Pipeline, run.Args, slog.Default()), rc)
	if err != nil {
		return fmt.Errorf("build plan: %w", err)
	}
	snap, err := claimPlanSnapshot(plan, rc, reg)
	if err != nil {
		return err
	}
	doc, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	_, err = out.Write(doc)
	return err
}

// safety: a node's pod plans again and compares its node's hash with the one
// the accepted plan holds, so it never runs a node that plan does not hold.
func claimPlanSnapshot(plan *sparkwing.Plan, rc sparkwing.RunContext, reg *sparkwing.Registration) (planSnapshot, error) {
	meta := planSnapshotMeta{Secrets: sparkwingruntime.ReflectSecretsField(reg)}
	if y := loadPipelineYAML(rc.Pipeline); y != nil {
		meta.PipelineRequires = y.Requires
	}
	snap, err := buildPlanSnapshot(plan, rc, meta)
	if err != nil {
		return snap, err
	}
	for i := range snap.Nodes {
		raw, err := json.Marshal(snap.Nodes[i])
		if err != nil {
			return snap, err
		}
		sum := sha256.Sum256(raw)
		snap.Nodes[i].SpecHash = "sha256:" + hex.EncodeToString(sum[:])
	}
	if c := plan.CheckoutValue(); c != nil {
		depth := max(c.Depth, 1)
		if c.FullHistory {
			depth = 0
		}
		snap.Source = &snapshotSource{Depth: depth, Tags: c.Tags, Submodules: c.Submodules, LFS: c.LFS}
	}
	return snap, nil
}

func claimNodeSpecHash(plan *sparkwing.Plan, rc sparkwing.RunContext, reg *sparkwing.Registration, nodeID string) (string, error) {
	snap, err := claimPlanSnapshot(plan, rc, reg)
	if err != nil {
		return "", err
	}
	for _, n := range snap.Nodes {
		if n.ID == nodeID {
			return n.SpecHash, nil
		}
	}
	return "", fmt.Errorf("the plan holds no node %q", nodeID)
}
