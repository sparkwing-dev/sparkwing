package client

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

var _ controllerwire.Controller = (*engineClient)(nil)

func (c *engineClient) Compatibility(ctx context.Context, version string) (controllerwire.Compatibility, error) {
	var result controllerwire.Compatibility
	err := c.request(ctx, "GET", "/api/v1/capabilities", "/api/v1/capabilities?engine_version="+url.QueryEscape(version), "", nil, &result)
	return result, err
}

func (c *engineClient) ReadRun(ctx context.Context, run string) (controllerwire.RunDetail, error) {
	var result controllerwire.RunDetail
	if !validSelector(run) {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "invalid run selector"}
	}
	err := c.request(ctx, "GET", "/api/v1/runs/{run}", "/api/v1/runs/"+url.PathEscape(run)+"", "", nil, &result)
	return result, err
}

func (c *engineClient) ReadSnapshot(ctx context.Context, run string) (controllerwire.ScheduleSnapshot, error) {
	var result controllerwire.ScheduleSnapshot
	if !validSelector(run) {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "invalid run selector"}
	}
	err := c.request(ctx, "GET", "/api/v1/runs/{run}/snapshot", "/api/v1/runs/"+url.PathEscape(run)+"/snapshot", "", nil, &result)
	return result, err
}

func (c *engineClient) AcceptPlan(ctx context.Context, run string, input controllerwire.PlanSubmission) (controllerwire.PlanAccepted, error) {
	var result controllerwire.PlanAccepted
	if !validSelector(run) {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "invalid run selector"}
	}
	err := c.request(ctx, "POST", "/api/v1/runs/{run}/plan", "/api/v1/runs/"+url.PathEscape(run)+"/plan", "", input, &result)
	return result, err
}

func (c *engineClient) Renew(ctx context.Context, claim string, input controllerwire.RenewalRequest) (controllerwire.RenewalResponse, error) {
	var result controllerwire.RenewalResponse
	if !validSelector(claim) {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "invalid claim selector"}
	}
	err := c.request(ctx, "POST", "/api/v1/claims/{claim}/heartbeat", "/api/v1/claims/"+url.PathEscape(claim)+"/heartbeat", "", input, &result)
	return result, err
}

func (c *engineClient) TransitionJob(ctx context.Context, run, job string, input controllerwire.JobTransitionRequest) (controllerwire.Applied, error) {
	var result controllerwire.Applied
	if !validSelector(run) {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "invalid run selector"}
	}
	if !validSelector(job) {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "invalid job selector"}
	}
	err := c.request(ctx, "POST", "/api/v1/runs/{run}/jobs/{job}/transition", "/api/v1/runs/"+url.PathEscape(run)+"/jobs/"+url.PathEscape(job)+"/transition", "", input, &result)
	return result, err
}

func (c *engineClient) ReconcileJob(ctx context.Context, run, job string, input controllerwire.ReconcileRequest) (controllerwire.ReconcileResponse, error) {
	var result controllerwire.ReconcileResponse
	if !validSelector(run) {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "invalid run selector"}
	}
	if !validSelector(job) {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "invalid job selector"}
	}
	err := c.request(ctx, "POST", "/api/v1/runs/{run}/jobs/{job}/reconcile", "/api/v1/runs/"+url.PathEscape(run)+"/jobs/"+url.PathEscape(job)+"/reconcile", "", input, &result)
	return result, err
}

func (c *engineClient) AcceptExpansion(ctx context.Context, run, job string, input controllerwire.ExpansionSubmission) (controllerwire.ExpansionAccepted, error) {
	var result controllerwire.ExpansionAccepted
	if !validSelector(run) {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "invalid run selector"}
	}
	if !validSelector(job) {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "invalid job selector"}
	}
	err := c.request(ctx, "POST", "/api/v1/runs/{run}/jobs/{job}/expansion", "/api/v1/runs/"+url.PathEscape(run)+"/jobs/"+url.PathEscape(job)+"/expansion", "", input, &result)
	return result, err
}

func (c *engineClient) Finish(ctx context.Context, run string, input controllerwire.FinishRequest) (controllerwire.Applied, error) {
	var result controllerwire.Applied
	if !validSelector(run) {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "invalid run selector"}
	}
	err := c.request(ctx, "POST", "/api/v1/runs/{run}/finish", "/api/v1/runs/"+url.PathEscape(run)+"/finish", "", input, &result)
	return result, err
}

func (c *engineClient) Cancel(ctx context.Context, run string, input controllerwire.CancelRequest) (controllerwire.Applied, error) {
	var result controllerwire.Applied
	if !validSelector(run) {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "invalid run selector"}
	}
	err := c.request(ctx, "POST", "/api/v1/runs/{run}/cancel", "/api/v1/runs/"+url.PathEscape(run)+"/cancel", "", input, &result)
	return result, err
}

func (c *engineClient) CancelJob(ctx context.Context, run, job string, input controllerwire.CancelRequest) (controllerwire.Applied, error) {
	var result controllerwire.Applied
	if !validSelector(run) || !validSelector(job) {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "invalid run or job selector"}
	}
	err := c.request(ctx, "POST", "/api/v1/runs/{run}/jobs/{job}/cancel", "/api/v1/runs/"+url.PathEscape(run)+"/jobs/"+url.PathEscape(job)+"/cancel", "", input, &result)
	return result, err
}

func (c *engineClient) ReadFailure(ctx context.Context, run, job string, generation int64) (*nodeproto.JobFailure, error) {
	if !validSelector(run) || !validSelector(job) || generation < 0 {
		return nil, &apierr.Error{Code: apierr.BadRequest, Message: "failure read requires valid run, job and source generation"}
	}
	var result *nodeproto.JobFailure
	err := c.request(ctx, "GET", "/api/v1/runs/{run}/jobs/{job}/failure", "/api/v1/runs/"+url.PathEscape(run)+"/jobs/"+url.PathEscape(job)+"/failure?source_generation="+strconv.FormatInt(generation, 10), "", nil, &result)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (c *engineClient) SyncLauncher(ctx context.Context, input controllerwire.LauncherSyncRequest) (controllerwire.LauncherSyncResponse, error) {
	var result controllerwire.LauncherSyncResponse
	err := c.request(ctx, "POST", "/api/v1/launcher/sync", "/api/v1/launcher/sync", "", input, &result)
	if err != nil {
		return result, err
	}
	for _, award := range result.Awards {
		if award.Capability != "" {
			return controllerwire.LauncherSyncResponse{}, &apierr.Error{Code: apierr.ProtocolError, Message: "launcher sync returned private bearer material"}
		}
	}
	return result, nil
}

func (c *engineClient) Exchange(ctx context.Context) (controllerwire.ExchangeResponse, error) {
	var result controllerwire.ExchangeResponse
	err := c.request(ctx, "POST", "/api/v1/claims/exchange", "/api/v1/claims/exchange", "", nil, &result)
	return result, err
}

func (c *engineClient) Retry(ctx context.Context, run string, input controllerwire.RetryRequest) (controllerwire.CreateRunResponse, error) {
	var result controllerwire.CreateRunResponse
	if !validSelector(run) {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "invalid run selector"}
	}
	if err := input.Validate(); err != nil {
		return result, err
	}
	err := c.request(ctx, "POST", "/api/v1/runs/{run}/retry", "/api/v1/runs/"+url.PathEscape(run)+"/retry", input.IdempotencyKey, input, &result)
	return result, err
}

func (c *engineClient) CreateRun(ctx context.Context, input controllerwire.CreateRunRequest) (controllerwire.CreateRunResponse, error) {
	var result controllerwire.CreateRunResponse
	err := c.request(ctx, "POST", "/api/v1/runs", "/api/v1/runs", input.IdempotencyKey, input, &result)
	return result, err
}

func validSelector(value string) bool {
	return value != "" && value != "." && value != ".." && !strings.ContainsAny(value, "\r\n\x00")
}

func (c *engineClient) Claim(ctx context.Context, input controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
	var result controllerwire.ClaimResponse
	pattern, path, err := claimPath(input, c.launcher)
	if err != nil {
		return result, err
	}
	err = c.request(ctx, "POST", pattern, path, "", input, &result)
	return result, err
}

func claimPath(input controllerwire.ClaimRequest, launcher bool) (string, string, error) {
	if len(input.Kinds) == 0 {
		return "", "", &apierr.Error{Code: apierr.BadRequest, Message: "claim requires at least one unit kind"}
	}
	seen := make(map[capability.UnitKind]bool, len(input.Kinds))
	for _, kind := range input.Kinds {
		if seen[kind] {
			return "", "", &apierr.Error{Code: apierr.BadRequest, Message: "duplicate claim unit kind"}
		}
		seen[kind] = true
		switch kind {
		case capability.UnitTrigger, capability.UnitNode:
		case capability.UnitPlan, capability.UnitEval:
			if !launcher {
				return "", "", &apierr.Error{Code: apierr.BadRequest, Message: "plan and eval claims require a launcher client"}
			}
		default:
			return "", "", &apierr.Error{Code: apierr.BadRequest, Message: "unknown claim unit kind"}
		}
	}
	if launcher {
		for _, kind := range input.Kinds {
			if kind != capability.UnitNode && kind != capability.UnitPlan && kind != capability.UnitEval {
				return "", "", &apierr.Error{Code: apierr.BadRequest, Message: "launcher claims require job, plan or eval units"}
			}
		}
		if input.RunID != "" && !validSelector(input.RunID) || input.JobID != "" && (!validSelector(input.JobID) || !validSelector(input.RunID)) {
			return "", "", &apierr.Error{Code: apierr.BadRequest, Message: "invalid named launcher claim"}
		}
		return "/api/v1/launcher/claim", "/api/v1/launcher/claim", nil
	}
	if input.JobID != "" {
		if !validSelector(input.RunID) || !validSelector(input.JobID) || len(input.Kinds) != 1 || input.Kinds[0] != capability.UnitNode {
			return "", "", &apierr.Error{Code: apierr.BadRequest, Message: "named job claim requires valid run and job selectors"}
		}
		return "/api/v1/runs/{run}/jobs/{job}/claim", "/api/v1/runs/" + url.PathEscape(input.RunID) + "/jobs/" + url.PathEscape(input.JobID) + "/claim", nil
	}
	if input.RunID != "" {
		if !validSelector(input.RunID) {
			return "", "", &apierr.Error{Code: apierr.BadRequest, Message: "invalid run selector"}
		}
		if len(input.Kinds) == 1 && input.Kinds[0] == capability.UnitTrigger {
			return "/api/v1/runs/{run}/claim", "/api/v1/runs/" + url.PathEscape(input.RunID) + "/claim", nil
		}
	}
	return "/api/v1/runners/claim", "/api/v1/runners/claim", nil
}
