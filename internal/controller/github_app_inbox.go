package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type githubDeliveryReceivedKey struct{}

// Existing event processors write HTTP outcomes; the inbox owns their retry policy.
type githubDeliveryOutcome struct {
	failure error
	digest  string
	header  http.Header
	status  int
	body    bytes.Buffer
}

func (o *githubDeliveryOutcome) ResponseError(err error) {
	if o.failure == nil {
		o.failure = err
	}
}

func (o *githubDeliveryOutcome) Header() http.Header { return o.header }
func (o *githubDeliveryOutcome) WriteHeader(status int) {
	if o.status == 0 {
		o.status = status
	}
}

func (o *githubDeliveryOutcome) Write(body []byte) (int, error) {
	if o.status == 0 {
		o.status = http.StatusOK
	}
	return o.body.Write(body)
}

func (o *githubDeliveryOutcome) retryReason() string {
	if o.failure != nil {
		return o.failure.Error()
	}
	if o.status == 0 || o.status >= 500 || o.status == http.StatusTooManyRequests || o.header.Get("Retry-After") != "" {
		return fmt.Sprintf("processing returned %d: %s", o.status, o.body.String())
	}
	var response githubAppWebhookResp
	if err := json.Unmarshal(o.body.Bytes(), &response); err == nil {
		for _, run := range response.Runs {
			if run.Status == "queued" {
				return "waiting for run admission"
			}
		}
	}
	return ""
}

func (s *Server) processNextGitHubDelivery(ctx context.Context, now time.Time) (*githubDeliveryOutcome, error) {
	queued, err := s.operator.ClaimGitHubDelivery(ctx, now)
	if err != nil || queued == nil {
		return nil, err
	}
	processing, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	processing = context.WithValue(processing, githubDeliveryReceivedKey{}, queued.ReceivedAt)
	r, err := http.NewRequestWithContext(processing, http.MethodPost, "/webhooks/github-app", nil)
	if err != nil {
		return nil, err
	}
	r.Header.Set("X-GitHub-Event", queued.Event)
	r.Header.Set("X-GitHub-Delivery", queued.ID)
	outcome := &githubDeliveryOutcome{digest: githubAppDeliveryDigest(queued.Body), header: make(http.Header)}
	s.processGitHubDelivery(outcome, r, *queued)
	reason := outcome.retryReason()
	if reason != "" {
		s.logger.Warn("github app delivery queued for retry", "delivery", queued.ID, "attempt", queued.Attempts, "reason", reason)
	} else if outcome.status >= 400 {
		s.logger.Warn("github app delivery refused", "delivery", queued.ID, "status", outcome.status, "result", outcome.body.String())
	}
	finished := time.Now()
	if now.After(finished) {
		finished = now
	}
	return outcome, s.operator.FinishGitHubDelivery(ctx, *queued, reason, finished)
}

func (s *Server) runGitHubInbox(ctx context.Context) {
	if s.githubApp == nil {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		for ctx.Err() == nil {
			outcome, err := s.processNextGitHubDelivery(ctx, time.Now())
			if err != nil {
				s.logger.Error("github app inbox processing failed", "err", err)
				break
			}
			if outcome == nil {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
