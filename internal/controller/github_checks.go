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

type githubCheckRepository struct {
	token, owner, name string
	private            bool
}

func (r *githubCheckReporter) repository(ctx context.Context, u githubCheckUpdate) (githubCheckRepository, error) {
	in, err := r.operator.GitHubAppInstallationTeam(ctx, u.installation)
	if err != nil {
		return githubCheckRepository{}, err
	}
	if in.Team != u.team || in.Suspended {
		return githubCheckRepository{}, fmt.Errorf("GitHub installation is not active for the run's team")
	}
	tok, err := r.client.InstallationTokenByIDs(ctx, u.installation, []int64{u.repository}, map[string]string{"checks": "write"})
	if err != nil {
		return githubCheckRepository{}, err
	}
	repo, err := r.client.RepositoryByID(ctx, tok.Token, u.repository)
	if err != nil {
		return githubCheckRepository{}, err
	}
	name, valid := store.ParseGitHubRepo(repo.FullName)
	if repo.ID != u.repository || !valid {
		return githubCheckRepository{}, fmt.Errorf("GitHub repository identity does not match the sealed run")
	}
	in, err = r.operator.GitHubAppInstallationTeam(ctx, u.installation)
	if err != nil {
		return githubCheckRepository{}, err
	}
	if in.Team != u.team || in.Suspended {
		return githubCheckRepository{}, fmt.Errorf("GitHub installation is not active for the run's team")
	}
	return githubCheckRepository{token: tok.Token, owner: name.Owner, name: name.Name, private: repo.Private}, nil
}

type githubCheckDetailOutput struct {
	output      githubapp.CheckRunOutput
	annotations []githubapp.CheckRunAnnotation
}

func (r *githubCheckReporter) writeCheckRun(ctx context.Context, tenant *store.Tenant, u githubCheckUpdate, run *store.Run, recorded int64) (int64, error) {
	repo, err := r.repository(ctx, u)
	if err != nil {
		return 0, err
	}
	check, details, err := r.checkRun(ctx, tenant, u, run, repo.private)
	if err != nil {
		return 0, err
	}
	id := recorded
	if id == 0 {
		if id, err = r.client.FindCheckRun(ctx, repo.token, repo.owner, repo.name, u.sha, check.Name, u.runID); err != nil {
			return 0, err
		}
	}
	// safety: GitHub appends each write's annotations, so a redelivery sends only those the check run lacks.
	sent := 0
	if details != nil && id != 0 && len(details.annotations) > 0 {
		if sent, err = r.client.CheckRunAnnotationCount(ctx, repo.token, repo.owner, repo.name, id); err != nil {
			return 0, err
		}
		sent = min(sent, len(details.annotations))
	}
	aggregate := check.Output
	for {
		var batch []githubapp.CheckRunAnnotation
		// safety: a repository can turn public between writes, so every write that carries details rereads its
		// visibility first. Anything but an explicit private:true, including a missing or null field, is public and
		// gets the aggregate output; a failed lookup returns the error so the report retries under the same rule.
		if details != nil {
			private, err := r.stillPrivate(ctx, repo, u.repository)
			if err != nil {
				return 0, err
			}
			if !private {
				details = nil
			}
		}
		check.Output = aggregate
		if details != nil {
			batch = details.annotations[sent:min(sent+githubapp.MaxCheckRunAnnotations, len(details.annotations))]
			out := details.output
			out.Annotations = batch
			check.Output = &out
		}
		if id == 0 {
			id, err = r.client.CreateCheckRun(ctx, repo.token, repo.owner, repo.name, check)
		} else {
			err = r.client.UpdateCheckRun(ctx, repo.token, repo.owner, repo.name, id, check)
		}
		if err != nil {
			return 0, err
		}
		sent += len(batch)
		if details == nil || sent >= len(details.annotations) {
			break
		}
	}
	if recorded != 0 {
		return 0, nil
	}
	return id, nil
}

func (r *githubCheckReporter) stillPrivate(ctx context.Context, repo githubCheckRepository, id int64) (bool, error) {
	current, err := r.client.RepositoryByID(ctx, repo.token, id)
	if err != nil {
		return false, err
	}
	if current.ID != id {
		return false, fmt.Errorf("GitHub repository identity does not match the sealed run")
	}
	return current.Private, nil
}

func (r *githubCheckReporter) checkRun(ctx context.Context, tenant *store.Tenant, u githubCheckUpdate, run *store.Run, private bool) (githubapp.CheckRun, *githubCheckDetailOutput, error) {
	link := githubRunTargetURL(r.dashboardURL, u.runID)
	check := githubapp.CheckRun{
		Name: "sparkwing/" + u.pipeline, HeadSHA: u.sha, DetailsURL: link, ExternalID: u.runID,
	}
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
		var steps []*store.NodeStep
		if run != nil {
			var err error
			if nodes, err = tenant.ListNodes(ctx, u.runID); err != nil {
				return githubapp.CheckRun{}, nil, err
			}
			if private {
				if steps, err = tenant.ListNodeSteps(ctx, u.runID); err != nil {
					return githubapp.CheckRun{}, nil, err
				}
			}
		}
		// safety: the text is always set, so an aggregate write replaces text an earlier private write left behind.
		check.Output = &githubapp.CheckRunOutput{
			Title:   githubCheckTitle(check.Conclusion),
			Summary: githubCheckSummary(run, nodes, check.Conclusion, link),
			Text:    githubCheckLinkLine("Job results stay on the run page.", link),
		}
		// safety: GitHub shows check output to everyone who can read the repository, so masked errors, summaries
		// and annotations go only where GitHub explicitly reports private:true. A public repository, or one whose
		// visibility GitHub omits or nulls, gets aggregate run state.
		if private {
			output, annotations := githubCheckDetails(run, nodes, steps, check.Conclusion, link)
			return check, &githubCheckDetailOutput{output: *output, annotations: annotations}, nil
		}
	}
	return check, nil, nil
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

// safety: names no job and carries no run text, so a public repository's check shows only aggregate state.
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
