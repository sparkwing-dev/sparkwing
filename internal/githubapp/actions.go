package githubapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// DispatchWorkflow sends once. An error without an HTTP response is an uncertain outcome.
func (c *Client) DispatchWorkflow(ctx context.Context, token, repository, workflow, ref, receipt, runner string) (int, error) {
	payload, err := json.Marshal(map[string]any{"ref": ref, "inputs": map[string]string{"receipt": receipt, "runner": runner}})
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.APIURL+"/repos/"+repository+"/actions/workflows/"+url.PathEscape(strings.TrimPrefix(workflow, ".github/workflows/"))+"/dispatches", strings.NewReader(string(payload)))
	if err != nil {
		return 0, err
	}
	c.apiHeaders(req, "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	sender := *c.cfg.HTTP
	sender.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	sending, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	response, err := sender.Do(req.WithContext(sending))
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	return response.StatusCode, nil
}

// WorkflowRun contains provider facts used to bind a dispatch receipt.
type WorkflowRun struct {
	ID         int64      `json:"id"`
	Attempt    int64      `json:"run_attempt"`
	Event      string     `json:"event"`
	Title      string     `json:"display_title"`
	Path       string     `json:"path"`
	SHA        string     `json:"head_sha"`
	Repository Repository `json:"repository"`
}

// ActionsRun reads authenticated provider metadata for the run in an OIDC token.
func (c *Client) ActionsRun(ctx context.Context, token, repository, id string) (WorkflowRun, error) {
	if n, err := strconv.ParseInt(id, 10, 64); err != nil || n <= 0 {
		return WorkflowRun{}, errors.New("githubapp: invalid run ID")
	}
	var run WorkflowRun
	err := c.getJSON(ctx, "/repos/"+repository+"/actions/runs/"+id, "Bearer "+token, &run)
	return run, err
}

// ActionsJobLabels finds the job whose check run GitHub attested in the OIDC token.
func (c *Client) ActionsJobLabels(ctx context.Context, token, repository, run, attempt, check string) ([]string, error) {
	for _, id := range []string{run, attempt, check} {
		if n, err := strconv.ParseInt(id, 10, 64); err != nil || n <= 0 {
			return nil, errors.New("githubapp: invalid job identity")
		}
	}
	for page := 1; ; page++ {
		var jobs struct {
			Jobs []struct {
				CheckURL string   `json:"check_run_url"`
				Labels   []string `json:"labels"`
			} `json:"jobs"`
		}
		err := c.getJSON(ctx, "/repos/"+repository+"/actions/runs/"+run+"/attempts/"+attempt+"/jobs?per_page=100&page="+strconv.Itoa(page), "Bearer "+token, &jobs)
		if err != nil {
			return nil, err
		}
		for _, job := range jobs.Jobs {
			if job.CheckURL == "https://api.github.com/repos/"+repository+"/check-runs/"+check {
				return job.Labels, nil
			}
		}
		if len(jobs.Jobs) < 100 {
			return nil, errors.New("githubapp: OIDC job absent from workflow attempt")
		}
	}
}

// CancelActionsRun cancels a known provider run without changing its Sparkwing award.
func (c *Client) CancelActionsRun(ctx context.Context, token, repository, run string) error {
	if n, err := strconv.ParseInt(run, 10, 64); err != nil || n <= 0 {
		return errors.New("githubapp: invalid run ID")
	}
	_, err := c.repoWrite(ctx, http.MethodPost, token, strings.Split(repository, "/")[0], strings.Split(repository, "/")[1], "/actions/runs/"+run+"/cancel", "cancel Actions run", nil)
	return err
}
