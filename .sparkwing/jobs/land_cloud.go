package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: these tracked public fixtures only look like credentials; the upload's secret-value scan still applies.
var cloudGateSecretShapedFixtures = []string{
	".gitleaks.toml",
	"charts/sparkwing-full/examples/external-secrets.yaml",
	"design/rewrite/proposals/thin-sdk-step1/measurements.json",
	"testdata/sdk-compat/*/compat-cache-key.txt",
}

type landCloudGate struct {
	sparkwing.Base
	sparkwing.Produces[string]
	RepoDir string
	Profile string
	Head    sparkwing.Ref[landHead]
}

func (j *landCloudGate) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "gate", j.run), nil
}

func (j *landCloudGate) run(ctx context.Context) (string, error) {
	head := j.Head.Get(ctx)
	git := gitIn(j.RepoDir)
	if err := requireLandHead(ctx, git, j.RepoDir, head); err != nil {
		return "", err
	}
	args := []string{"run", "gate", "--sw-route", "cloud", "--profile", j.Profile, "-o", "json"}
	allowed, err := git(ctx, append([]string{"ls-files", "--"}, cloudGateSecretShapedFixtures...)...)
	if err != nil {
		return "", err
	}
	if allowed != "" {
		args = append(args, "--allow-secret-file", strings.Join(strings.Fields(allowed), ","))
	}
	// safety: full mode covers every package; the public base keeps policy and lint scoped to this change.
	result, err := sparkwing.Exec(ctx, "sparkwing", append(args, "--", "--full", "--base", head.Base)...).Dir(j.RepoDir).Run()
	if err != nil {
		return "", fmt.Errorf("land: Cloud gate failed; inspect the run with --profile %s: %w", j.Profile, err)
	}
	if err := requireLandHead(ctx, git, j.RepoDir, head); err != nil {
		return "", err
	}
	return cloudGateReceipt(result.Stdout)
}

func requireLandHead(ctx context.Context, git gitRunner, dir string, head landHead) error {
	if err := requireClean(ctx, git, dir); err != nil {
		return err
	}
	commit, err := git(ctx, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if commit != head.Commit {
		return errors.New("land: HEAD changed while running the Cloud gate; rerun sparkwing run land")
	}
	return nil
}

func cloudGateReceipt(output string) (string, error) {
	output = strings.TrimSpace(output)
	if last := strings.LastIndexByte(output, '\n'); last >= 0 {
		output = output[last+1:]
	}
	var receipt struct {
		RunID   string `json:"run_id"`
		Number  int64  `json:"number"`
		Outcome string `json:"outcome"`
	}
	if err := json.Unmarshal([]byte(output), &receipt); err != nil || receipt.RunID == "" || receipt.Number <= 0 || receipt.Outcome != "success" {
		return "", errors.New("land: Cloud gate returned no successful run receipt; rerun sparkwing run land")
	}
	return receipt.RunID, nil
}
