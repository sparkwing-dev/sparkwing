package sdkserve

import (
	"context"
	"fmt"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// DescribeDoc is the describe result: every registered pipeline and its
// flags, as docs/schemas/describe.schema.json declares the root.
type DescribeDoc struct {
	Protocol  string                       `json:"protocol"`
	SDK       *SDKInfo                     `json:"sdk,omitempty"`
	Pipelines []sparkwing.DescribePipeline `json:"pipelines"`
}

// SDKInfo names the SDK that answered describe.
type SDKInfo struct {
	Language string `json:"language"`
	Version  string `json:"version"`
}

func describe(_ context.Context, s *server, _ nodeproto.Request) (any, error) {
	pipelines, err := sparkwingruntime.DescribeAll() //nolint:contextcheck // describe plans each pipeline with empty args, which no request cancels
	if err != nil {
		return nil, err
	}
	doc := DescribeDoc{Protocol: nodeproto.Version, Pipelines: pipelines}
	if s.cfg.SDKVersion != "" {
		doc.SDK = &SDKInfo{Language: "go", Version: s.cfg.SDKVersion}
	}
	return doc, nil
}

func plan(ctx context.Context, s *server, req nodeproto.Request) (any, error) {
	s.mu.Lock()
	seen := s.planSeen
	s.planSeen = true
	s.mu.Unlock()
	if seen {
		return nil, &nodeproto.Error{Code: nodeproto.CodeBadRequest, Message: "plan arrives at most once per process"}
	}
	reg, ok := sparkwing.Lookup(req.Pipeline)
	if !ok {
		return nil, &nodeproto.Error{Code: nodeproto.CodeUnknownPipeline, Message: fmt.Sprintf("pipeline %q is not registered", req.Pipeline)}
	}
	rc := runContext(req.Pipeline, req.Run)
	sparkwing.SetGit(rc.Git)
	built, err := reg.Invoke(sparkwingruntime.WithLogger(ctx, frameLogger{s}), req.Args, rc)
	if err != nil {
		code := nodeproto.CodePlanFailed
		// hack: Invoke reports args that fail the declared inputs under this
		// prefix, and the SDK has no typed error to tell them from plan code's.
		if strings.HasPrefix(err.Error(), fmt.Sprintf("inputs for pipeline %q: ", req.Pipeline)) {
			code = nodeproto.CodeInvalidArgs
		}
		return nil, &nodeproto.Error{Code: code, Message: err.Error()}
	}
	doc, err := s.cfg.PlanDoc(built, rc, reg)
	if err != nil {
		return nil, &nodeproto.Error{Code: nodeproto.CodePlanFailed, Message: err.Error()}
	}
	s.mu.Lock()
	s.planned = &Planned{Registration: reg, Plan: built, Run: rc, Doc: doc}
	s.mu.Unlock()
	return doc, nil
}

func runContext(pipeline string, run *nodeproto.RunContext) sparkwing.RunContext {
	rc := sparkwing.RunContext{
		RunID:     run.RunID,
		Pipeline:  pipeline,
		StartedAt: run.StartedAt,
		NoCache:   run.NoCache,
		DryRun:    run.DryRun,
	}
	workDir := sparkwing.CurrentRuntime().WorkDir
	if g := run.Git; g != nil {
		rc.Git = sparkwing.NewGit(workDir, g.SHA, g.Branch, g.DefaultBranch, g.Repo, g.RepoURL)
	} else {
		rc.Git = sparkwing.NewGit(workDir, "", "", "", "", "")
	}
	if t := run.Trigger; t != nil {
		rc.Trigger = sparkwing.TriggerInfo{Source: t.Source, User: t.User}
		if pr := t.PullRequest; pr != nil {
			rc.Trigger.PullRequest = &sparkwing.PullRequest{
				Number: pr.Number, Action: pr.Action,
				BaseRef: pr.BaseRef, BaseSHA: pr.BaseSHA, HeadRef: pr.HeadRef, HeadSHA: pr.HeadSHA,
			}
		}
	}
	return rc
}
