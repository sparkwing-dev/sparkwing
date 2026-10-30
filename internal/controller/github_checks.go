package controller

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type githubCheckPhase int

const (
	githubCheckQueued githubCheckPhase = iota + 1
	githubCheckInProgress
	githubCheckCompleted
)

func githubCheckPhaseFor(runStatus string) githubCheckPhase {
	switch runStatus {
	case "pending":
		return githubCheckQueued
	case "running":
		return githubCheckInProgress
	default:
		return githubCheckCompleted
	}
}

func githubCheckConclusion(runStatus string) string {
	switch runStatus {
	case "success":
		return "success"
	case "dry_run":
		return "neutral"
	case "cancelled":
		return "cancelled"
	case "timed_out":
		return "timed_out"
	default:
		return "failure"
	}
}

type githubCheckUpdate struct {
	installation int64
	repository   int64
	sha          string
	pipeline     string
	runID        string
	runStatus    string
	team         store.Team
}

func (u githubCheckUpdate) phase() githubCheckPhase { return githubCheckPhaseFor(u.runStatus) }

type githubCheckReporter struct {
	client        *githubapp.Client
	resolveTenant func(context.Context, store.Team) (TenantHandle, error)
	operator      *store.Operator
	dashboardURL  string
}

func (r *githubCheckReporter) deliver(ctx context.Context, u githubCheckUpdate) error {
	tenant, err := scopedTenantPort[*store.Tenant](ctx, u.team, r.resolveTenant)
	if err != nil {
		return err
	}
	return tenant.ReportGitHubCheck(ctx, u.runID, func(ctx context.Context, id int64) (int64, error) {
		run, err := tenant.GetRun(ctx, u.runID)
		if err != nil {
			return 0, err
		}
		if u.phase() < githubCheckPhaseFor(run.Status) {
			return 0, nil
		}
		return r.writeCheckRun(ctx, tenant, u, run, id)
	})
}

func (r *githubCheckReporter) repository(ctx context.Context, u githubCheckUpdate) (string, string, string, error) {
	in, err := r.operator.GitHubAppInstallationTeam(ctx, u.installation)
	if err != nil {
		return "", "", "", err
	}
	if in.Team != u.team || in.Suspended {
		return "", "", "", fmt.Errorf("GitHub installation is not active for the run's team")
	}
	tok, err := r.client.InstallationTokenByIDs(ctx, u.installation, []int64{u.repository}, map[string]string{"checks": "write"})
	if err != nil {
		return "", "", "", err
	}
	repo, err := r.client.RepositoryByID(ctx, tok.Token, u.repository)
	if err != nil {
		return "", "", "", err
	}
	name, valid := store.ParseGitHubRepo(repo.FullName)
	if repo.ID != u.repository || !valid {
		return "", "", "", fmt.Errorf("GitHub repository identity does not match the sealed run")
	}
	in, err = r.operator.GitHubAppInstallationTeam(ctx, u.installation)
	if err != nil {
		return "", "", "", err
	}
	if in.Team != u.team || in.Suspended {
		return "", "", "", fmt.Errorf("GitHub installation is not active for the run's team")
	}
	return tok.Token, name.Owner, name.Name, nil
}

func (r *githubCheckReporter) writeCheckRun(ctx context.Context, tenant *store.Tenant, u githubCheckUpdate, run *store.Run, id int64) (int64, error) {
	token, owner, repo, err := r.repository(ctx, u)
	if err != nil {
		return 0, err
	}
	check, err := r.checkRun(ctx, tenant, u, run)
	if err != nil {
		return 0, err
	}
	if id == 0 {
		id, err = r.client.FindCheckRun(ctx, token, owner, repo, u.sha, check.Name, u.runID)
		if err != nil {
			return 0, err
		}
		if id == 0 {
			id, err = r.client.CreateCheckRun(ctx, token, owner, repo, check)
		} else {
			err = r.client.UpdateCheckRun(ctx, token, owner, repo, id, check)
		}
		if err != nil {
			return 0, err
		}
		return id, nil
	}
	return 0, r.client.UpdateCheckRun(ctx, token, owner, repo, id, check)
}

func (r *githubCheckReporter) checkRun(ctx context.Context, tenant *store.Tenant, u githubCheckUpdate, run *store.Run) (githubapp.CheckRun, error) {
	link := githubRunTargetURL(r.dashboardURL, u.runID)
	check := githubapp.CheckRun{
		Name: "sparkwing/" + u.pipeline, HeadSHA: u.sha, DetailsURL: link, ExternalID: u.runID,
	}
	var err error
	switch u.phase() {
	case githubCheckQueued:
		check.Status = "queued"
		check.Output = &githubapp.CheckRunOutput{Title: "Queued", Summary: githubCheckLinkLine("Waiting for a runner.", link)}
	case githubCheckInProgress:
		check.Status = "in_progress"
		if run != nil && !run.StartedAt.IsZero() {
			started := run.StartedAt
			check.StartedAt = &started
		}
		check.Output = &githubapp.CheckRunOutput{Title: "Running", Summary: githubCheckLinkLine("Running.", link)}
	default:
		check.Status, check.Conclusion = "completed", githubCheckConclusion(u.runStatus)
		completed := time.Now().UTC()
		if run != nil && run.FinishedAt != nil {
			completed = *run.FinishedAt
		}
		check.CompletedAt = &completed
		var nodes []*store.Node
		if run != nil {
			if nodes, err = tenant.ListNodes(ctx, u.runID); err != nil {
				return githubapp.CheckRun{}, err
			}
		}
		check.Output = &githubapp.CheckRunOutput{
			Title:   githubCheckTitle(check.Conclusion),
			Summary: githubCheckSummary(run, nodes, check.Conclusion, link),
		}
	}
	return check, nil
}

func githubCheckTitle(conclusion string) string {
	switch conclusion {
	case "success":
		return "Passed"
	case "neutral":
		return "Dry run completed"
	case "cancelled":
		return "Cancelled"
	case "timed_out":
		return "Timed out"
	default:
		return "Failed"
	}
}

func githubCheckLinkLine(text, link string) string {
	if link == "" {
		return text
	}
	return text + " [Open the run in Sparkwing](" + link + ")."
}

// safety: GitHub sees only aggregate run state; details stay on the authenticated run page.
func githubCheckSummary(run *store.Run, nodes []*store.Node, conclusion, link string) string {
	var summary strings.Builder
	summary.WriteString("**" + githubCheckTitle(conclusion) + "**")
	if run != nil && run.FinishedAt != nil && !run.StartedAt.IsZero() {
		summary.WriteString(" in " + run.FinishedAt.Sub(run.StartedAt).Round(time.Second).String())
	}
	summary.WriteString(".")
	if link != "" {
		summary.WriteString(" [Open the run in Sparkwing](" + link + ").")
	}
	counts := map[string]int{}
	for _, n := range nodes {
		outcome := n.Outcome
		if outcome == "" {
			outcome = n.Status
		}
		switch outcome {
		case "success", "failed", "satisfied", "cached", "skipped", "cancelled", "skipped-concurrent", "superseded", "pending", "running":
		default:
			outcome = "other"
		}
		counts[outcome]++
	}
	if len(counts) > 0 {
		outcomes := make([]string, 0, len(counts))
		for outcome := range counts {
			outcomes = append(outcomes, outcome)
		}
		slices.Sort(outcomes)
		summary.WriteString("\n\nNodes: ")
		for i, outcome := range outcomes {
			if i > 0 {
				summary.WriteString(", ")
			}
			fmt.Fprintf(&summary, "%d %s", counts[outcome], outcome)
		}
		summary.WriteString(".")
	}
	return summary.String()
}

const githubCheckTimeout = 10 * time.Second

func githubRunTargetURL(baseURL, runID string) string {
	if baseURL == "" {
		return ""
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	if (scheme != "http" && scheme != "https") ||
		u.Hostname() == "" ||
		u.User != nil ||
		u.RawQuery != "" ||
		u.ForceQuery ||
		u.Fragment != "" ||
		strings.Contains(baseURL, "#") {
		return ""
	}
	u.Scheme = scheme
	u.Path = strings.TrimRight(u.Path, "/") + "/runs"
	u.RawPath = ""
	q := url.Values{}
	q.Set("run", runID)
	u.RawQuery = q.Encode()
	return u.String()
}
