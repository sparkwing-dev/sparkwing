package wingd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

const defaultJevEndpoint = "https://api.typesafe.ai/v1/systemone"

type JevCandidate struct {
	Pipeline            string                 `json:"pipeline,omitempty"`
	Class               string                 `json:"class"`
	Resources           wingwire.HostResources `json:"resources"`
	ExpectedDurationMS  int64                  `json:"expected_duration_ms,omitempty"`
	ExpectedP99MS       int64                  `json:"expected_p99_ms"`
	DurationSampleCount int                    `json:"duration_sample_count"`
}

type JevState struct {
	Candidate JevCandidate      `json:"candidate"`
	Holders   []wingwire.Holder `json:"holders,omitempty"`
	Waiters   []wingwire.Waiter `json:"waiters,omitempty"`
}

type JevAnswer struct {
	Admit       bool
	Choice      string
	Confidence  float64
	Probability float64
	Model       string
}

// JevAdvisor is injectable so the scheduling boundary can be exercised
// without a live service. Implementations return advice, never a grant.
type JevAdvisor interface {
	Advise(context.Context, JevState) (JevAnswer, error)
}

type typesafeJevAdvisor struct {
	apiKey   string
	endpoint string
	model    string
	client   *http.Client
}

func newTypeSafeJevAdvisor(apiKey string, policy JevPolicy) JevAdvisor {
	if strings.TrimSpace(apiKey) == "" {
		return nil
	}
	endpoint := policy.Endpoint
	if endpoint == "" {
		endpoint = defaultJevEndpoint
	}
	model := policy.Model
	if model == "" {
		model = "jev-latest"
	}
	return &typesafeJevAdvisor{apiKey: apiKey, endpoint: endpoint, model: model, client: http.DefaultClient}
}

func (a *typesafeJevAdvisor) Advise(ctx context.Context, state JevState) (JevAnswer, error) {
	body := struct {
		State     JevState                  `json:"state"`
		Model     string                    `json:"model"`
		Questions map[string]map[string]any `json:"questions"`
	}{
		State: state,
		Model: a.model,
		Questions: map[string]map[string]any{
			"admission": {
				"type":         "choice",
				"instructions": "Choose whether the candidate should use currently spare capacity ahead of older work. Prefer admit_short_backfill when the candidate is latency-sensitive, strongly measured as short, and unlikely to materially worsen the older work's wait. Prefer wait when evidence is weak or the queue should recover first. Hard resource and semaphore safety is enforced separately by code.",
				"criteria": map[string]string{
					"admit_short_backfill": "Use spare capacity now for a bounded short backfill.",
					"wait":                 "Keep the candidate behind the older reservation.",
				},
			},
		},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return JevAnswer{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint, bytes.NewReader(encoded))
	if err != nil {
		return JevAnswer{}, err
	}
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := a.client.Do(req)
	if err != nil {
		return JevAnswer{}, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		limited, readErr := io.ReadAll(io.LimitReader(res.Body, 1024))
		if readErr != nil {
			return JevAnswer{}, fmt.Errorf("typesafe systemone: %s: read response body: %w", res.Status, readErr)
		}
		return JevAnswer{}, fmt.Errorf("typesafe systemone: %s: %s", res.Status, strings.TrimSpace(string(limited)))
	}
	var decoded struct {
		Model   string `json:"model"`
		Answers struct {
			Admission struct {
				Type          string             `json:"type"`
				Choice        string             `json:"choice"`
				Probabilities map[string]float64 `json:"probabilities"`
				Confidence    float64            `json:"confidence"`
			} `json:"admission"`
		} `json:"answers"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&decoded); err != nil {
		return JevAnswer{}, fmt.Errorf("decode typesafe systemone response: %w", err)
	}
	answer := decoded.Answers.Admission
	if answer.Type != "choice" || (answer.Choice != "admit_short_backfill" && answer.Choice != "wait") {
		return JevAnswer{}, fmt.Errorf("typesafe systemone returned an invalid admission choice %q", answer.Choice)
	}
	return JevAnswer{Admit: answer.Choice == "admit_short_backfill", Choice: answer.Choice, Confidence: answer.Confidence, Probability: answer.Probabilities[answer.Choice], Model: decoded.Model}, nil
}

func (d *Daemon) jevReservationBypass(req *wingwire.AdmissionRequest, resources wingwire.HostResources) bool {
	policy := d.cfg.admissionPolicy()
	if policy.Mode != "jev" || d.jevAdvisor == nil || req.ExpectedP99MS <= 0 || req.SampleCount < 3 ||
		time.Duration(req.ExpectedP99MS)*time.Millisecond > policy.Jev.maxBackfill() {
		return false
	}
	d.mu.Lock()
	queue := d.buildQueueStateLocked()
	d.mu.Unlock()
	if len(queue.Waiters) == 0 {
		return false
	}
	needsAdvice := false
	for _, waiter := range queue.Waiters {
		baseline := policy.Scheduling.BackfillDelayFor(admission.WorkloadClass(waiter.Class)).Milliseconds()
		if waiter.BackfillCount > 0 && waiter.BackfillDelayMS+req.ExpectedP99MS > baseline {
			needsAdvice = true
			break
		}
	}
	if !needsAdvice {
		return false
	}
	state := JevState{
		Candidate: JevCandidate{
			Pipeline:            req.Pipeline,
			Class:               req.Class,
			Resources:           resources,
			ExpectedDurationMS:  req.ExpectedDurationMS,
			ExpectedP99MS:       req.ExpectedP99MS,
			DurationSampleCount: req.SampleCount,
		},
		Holders: queue.Holders,
		Waiters: queue.Waiters,
	}
	ctx, cancel := context.WithTimeout(context.Background(), policy.Jev.timeout())
	defer cancel()
	answer, err := d.jevAdvisor.Advise(ctx, state)
	d.mu.Lock()
	d.jevAttempts++
	if err != nil || !policy.Jev.accepts(answer) {
		d.jevFallbacks++
		d.mu.Unlock()
		if err != nil {
			d.cfg.logf("jev admission advice unavailable; using auto: %v", err)
		}
		return false
	}
	if answer.Admit {
		d.jevAdmits++
	}
	d.mu.Unlock()
	return answer.Admit
}

func (p JevPolicy) accepts(answer JevAnswer) bool {
	return answer.Confidence >= p.minConfidence() && answer.Probability >= p.minProbability()
}

func (p JevPolicy) timeout() time.Duration {
	if p.Timeout > 0 {
		return p.Timeout
	}
	return 300 * time.Millisecond
}

func (p JevPolicy) minConfidence() float64 {
	if p.MinConfidence > 0 {
		return p.MinConfidence
	}
	return 0.5
}

func (p JevPolicy) minProbability() float64 {
	if p.MinProbability > 0 {
		return p.MinProbability
	}
	return 0.7
}

func (p JevPolicy) maxBackfill() time.Duration {
	if p.MaxBackfill > 0 {
		return p.MaxBackfill
	}
	return 5 * time.Second
}
