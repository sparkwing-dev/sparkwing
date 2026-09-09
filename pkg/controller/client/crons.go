package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/sparkwing-dev/sparkwing/internal/crons"
)

// CronRepoRequest is the body of PUT /api/v1/crons/repos: one repository's
// controller schedules as they should stand after the call. A schedule the body
// does not carry is withdrawn.
type CronRepoRequest struct {
	RepoURL string `json:"repo_url"`
	Branch  string `json:"branch,omitempty"`
	// SHA pins every fire to one commit. Follow instead clones the branch
	// tip at each fire, and ignores SHA.
	SHA       string             `json:"sha,omitempty"`
	Follow    bool               `json:"follow,omitempty"`
	Schedules []CronRepoSchedule `json:"schedules"`
}

// CronRepoSchedule is one pushed entry, spelled the way the repository declares
// it. CatchUp is a Go duration such as "6h".
type CronRepoSchedule struct {
	Pipeline string            `json:"pipeline"`
	Name     string            `json:"name,omitempty"`
	Cron     string            `json:"cron"`
	TZ       string            `json:"tz,omitempty"`
	Overlap  string            `json:"overlap,omitempty"`
	CatchUp  string            `json:"catch_up,omitempty"`
	Args     map[string]string `json:"args,omitempty"`
}

// CronReposResponse is what a push returns: the rows as they now stand, and the
// display names of the schedules the push withdrew.
type CronReposResponse struct {
	Schedules []crons.ScheduleView `json:"schedules"`
	Withdrawn []string             `json:"withdrawn,omitempty"`
}

// CronRepoDeleteResponse is what a repository delete returns.
type CronRepoDeleteResponse struct {
	RepoURL string `json:"repo_url"`
	Removed int    `json:"removed"`
}

// CronOverrideRequest is the body of PUT /api/v1/crons/{id}/override. A field
// left empty keeps what the repository declared; Args replaces the declared set
// whole.
type CronOverrideRequest struct {
	Cron    string            `json:"cron,omitempty"`
	TZ      string            `json:"tz,omitempty"`
	Overlap string            `json:"overlap,omitempty"`
	CatchUp string            `json:"catch_up,omitempty"`
	Args    map[string]string `json:"args,omitempty"`
}

// ListCrons returns the controller's scheduler health and every schedule pushed
// to it.
func (c *Client) ListCrons(ctx context.Context) (*crons.OverviewView, error) {
	var out crons.OverviewView
	if err := c.getJSON(ctx, c.baseURL+"/api/v1/crons", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetCron returns one schedule, its recent fires and the instants it matches
// next. The name may be a schedule id or any name `crons list` prints.
func (c *Client) GetCron(ctx context.Context, name string) (*crons.DetailView, error) {
	var out crons.DetailView
	if err := c.getJSON(ctx, c.baseURL+"/api/v1/crons/"+url.PathEscape(name), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PutCronRepo replaces one repository's controller schedules and returns the
// rows as they now stand.
func (c *Client) PutCronRepo(ctx context.Context, req CronRepoRequest) (*CronReposResponse, error) {
	var out CronReposResponse
	if err := c.put(ctx, "/api/v1/crons/repos", req, http.StatusOK, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteCronRepo removes every schedule pushed for one repository URL.
func (c *Client) DeleteCronRepo(ctx context.Context, repoURL string) (*CronRepoDeleteResponse, error) {
	q := url.Values{"repo_url": []string{repoURL}}
	var out CronRepoDeleteResponse
	if err := c.deleteJSON(ctx, "/api/v1/crons/repos?"+q.Encode(), http.StatusOK, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PauseCron stops a schedule firing without losing its history.
func (c *Client) PauseCron(ctx context.Context, name string) (*crons.ScheduleView, error) {
	return c.cronAction(ctx, name, "pause")
}

// ResumeCron lets a paused schedule fire again from its next due instant.
func (c *Client) ResumeCron(ctx context.Context, name string) (*crons.ScheduleView, error) {
	return c.cronAction(ctx, name, "resume")
}

// DisarmCron removes one schedule and its history, and returns the row as it
// stood.
func (c *Client) DisarmCron(ctx context.Context, name string) (*crons.ScheduleView, error) {
	return c.cronAction(ctx, name, "disarm")
}

func (c *Client) cronAction(ctx context.Context, name, action string) (*crons.ScheduleView, error) {
	var out crons.ScheduleEnvelope
	path := fmt.Sprintf("/api/v1/crons/%s/%s", url.PathEscape(name), action)
	if err := c.post(ctx, path, nil, http.StatusOK, &out); err != nil {
		return nil, err
	}
	return &out.Schedule, nil
}

// RunCronNow launches a schedule's pipeline immediately and returns the run it
// started alongside the schedule.
func (c *Client) RunCronNow(ctx context.Context, name string) (*crons.RunEnvelope, error) {
	var out crons.RunEnvelope
	path := fmt.Sprintf("/api/v1/crons/%s/run", url.PathEscape(name))
	if err := c.post(ctx, path, nil, http.StatusOK, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SetCronOverride lays the controller's own values over what the repository
// declared for one schedule.
func (c *Client) SetCronOverride(ctx context.Context, name string, req CronOverrideRequest) (*crons.ScheduleView, error) {
	var out crons.ScheduleEnvelope
	path := fmt.Sprintf("/api/v1/crons/%s/override", url.PathEscape(name))
	if err := c.put(ctx, path, req, http.StatusOK, &out); err != nil {
		return nil, err
	}
	return &out.Schedule, nil
}

// ClearCronOverride returns one schedule to what the repository declared.
func (c *Client) ClearCronOverride(ctx context.Context, name string) (*crons.ScheduleView, error) {
	var out crons.ScheduleEnvelope
	path := fmt.Sprintf("/api/v1/crons/%s/override", url.PathEscape(name))
	if err := c.deleteJSON(ctx, path, http.StatusOK, &out); err != nil {
		return nil, err
	}
	return &out.Schedule, nil
}
