package receipt

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type Receipt struct {
	RunID      string     `json:"run_id"`
	Pipeline   string     `json:"pipeline"`
	GitSHA     string     `json:"git_sha"`
	Status     string     `json:"status"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	DurationMS int64      `json:"duration_ms"`
	Identity   Identity   `json:"identity"`

	Invocation map[string]any `json:"invocation,omitempty"`
	Steps      []Step         `json:"steps"`
	Cost       Cost           `json:"cost"`
	ReceiptSHA string         `json:"receipt_sha"`
	// Store names the runs store the receipt was read from, and is excluded
	// from ReceiptSHA: where a copy of the run sits says nothing about what
	// ran, so the same run yields the same hash from any store.
	Store string `json:"store,omitempty"`
}

type Identity struct {
	PipelineVersionHash string            `json:"pipeline_version_hash"`
	InputsHash          string            `json:"inputs_hash"`
	PlanHash            string            `json:"plan_hash"`
	OutputsHash         map[string]string `json:"outputs_hash"`
}

type Step struct {
	ID         string `json:"id"`
	NodeID     string `json:"node_id"`
	DurationMS int64  `json:"duration_ms"`
	Outcome    string `json:"outcome"`
	SkipReason string `json:"skip_reason,omitempty"`
}

type Cost struct {
	Currency     string `json:"currency"`
	ComputeCents int64  `json:"compute_cents"`
	RateSource   string `json:"rate_source"`
	Settled      bool   `json:"settled"`
}

func BuildReceipt(run *store.Run, nodes []*store.Node, rate float64, rateSource string) Receipt {
	if run == nil {
		return Receipt{}
	}
	r := Receipt{
		RunID:      run.ID,
		Pipeline:   run.Pipeline,
		GitSHA:     run.GitSHA,
		Status:     run.Status,
		StartedAt:  run.StartedAt,
		FinishedAt: run.FinishedAt,

		Invocation: store.RedactInvocation(run.Invocation),
	}
	if run.FinishedAt != nil {
		r.DurationMS = run.FinishedAt.Sub(run.StartedAt).Milliseconds()
	}
	r.Identity = buildIdentity(run, nodes)
	r.Steps = buildSteps(nodes)
	r.Cost = buildCost(nodes, rate, rateSource)
	r.ReceiptSHA = computeReceiptSHA(r)
	return r
}

func buildIdentity(run *store.Run, nodes []*store.Node) Identity {
	id := Identity{
		PipelineVersionHash: hashBytes(run.PlanSnapshot),
		PlanHash:            planTopologyHash(nodes),
		OutputsHash:         outputsHashes(nodes),
	}
	if !containsNamedArg(run.Args, run.SecretArgNames()) {
		id.InputsHash = hashCanonical(run.Args)
	}
	return id
}

func containsNamedArg(args map[string]string, names []string) bool {
	for _, name := range names {
		if _, ok := args[name]; ok {
			return true
		}
	}
	return false
}

func planTopologyHash(nodes []*store.Node) string {
	type edge struct {
		ID   string   `json:"id"`
		Deps []string `json:"deps"`
	}
	edges := make([]edge, 0, len(nodes))
	for _, n := range nodes {
		deps := append([]string(nil), n.Deps...)
		sort.Strings(deps)
		edges = append(edges, edge{ID: n.NodeID, Deps: deps})
	}
	sort.Slice(edges, func(i, j int) bool { return edges[i].ID < edges[j].ID })
	return hashCanonical(edges)
}

func outputsHashes(nodes []*store.Node) map[string]string {
	out := make(map[string]string, len(nodes))
	for _, n := range nodes {
		if len(n.Output) == 0 {
			continue
		}
		out[n.NodeID] = hashBytes(n.Output)
	}
	return out
}

func buildSteps(nodes []*store.Node) []Step {
	steps := make([]Step, 0, len(nodes))
	for _, n := range nodes {
		s := Step{ID: n.NodeID, NodeID: n.NodeID, Outcome: stepOutcome(n)}
		if n.StartedAt != nil && n.FinishedAt != nil {
			s.DurationMS = n.FinishedAt.Sub(*n.StartedAt).Milliseconds()
		}
		if s.Outcome == "skipped" {
			s.SkipReason = n.StatusDetail
		}
		steps = append(steps, s)
	}
	return steps
}

func stepOutcome(n *store.Node) string {
	if n.Outcome != "" {
		return n.Outcome
	}
	if n.Status == "skipped" {
		return "skipped"
	}
	return n.Status
}

func buildCost(nodes []*store.Node, rate float64, rateSource string) Cost {
	c := Cost{Currency: "USD", RateSource: rateSource, Settled: false}
	if rate <= 0 {
		return c
	}
	var totalSec float64
	for _, n := range nodes {
		if n.StartedAt == nil || n.FinishedAt == nil {
			continue
		}
		switch n.Outcome {
		case "skipped", "cancelled":
			continue
		}
		d := n.FinishedAt.Sub(*n.StartedAt).Seconds()
		if d <= 0 {
			continue
		}
		totalSec += d
	}
	if totalSec <= 0 {
		return c
	}
	dollars := (totalSec / 3600.0) * rate
	c.ComputeCents = int64(dollars*100 + 0.5)
	return c
}

func computeReceiptSHA(r Receipt) string {
	r.ReceiptSHA = ""
	r.Store = ""
	return hashCanonical(r)
}

func hashCanonical(v any) string {
	buf, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return hashBytes(buf)
}

func hashBytes(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
