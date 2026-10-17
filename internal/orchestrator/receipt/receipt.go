package receipt

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// BuildReceipt hashes a redacted reporting view and its job observations.
func BuildReceipt(run *store.Run, nodes []*store.Node, rate float64, rateSource string) controllerwire.Receipt {
	if run == nil {
		return controllerwire.Receipt{}
	}
	r := controllerwire.Receipt{
		RunID:      run.ID,
		Pipeline:   run.Pipeline,
		GitSHA:     run.GitSHA,
		Status:     run.Status,
		StartedAt:  &run.StartedAt,
		FinishedAt: run.FinishedAt,
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

func buildIdentity(run *store.Run, nodes []*store.Node) controllerwire.ReceiptIdentity {
	id := controllerwire.ReceiptIdentity{
		PipelineVersionHash: hashBytes(run.PlanSnapshot),
		PlanHash:            planTopologyHash(nodes),
		OutputsHash:         outputsHashes(nodes),
	}
	if names, known := run.Invocation[store.InvocationSecretArgsKey].([]string); known && names != nil && !containsNamedArg(run.Args, names) {
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
		if n.OutputDigest == "" {
			continue
		}
		out[n.NodeID] = "sha256:" + n.OutputDigest
	}
	return out
}

func buildSteps(nodes []*store.Node) []controllerwire.ReceiptStep {
	steps := make([]controllerwire.ReceiptStep, 0, len(nodes))
	for _, n := range nodes {
		s := controllerwire.ReceiptStep{ID: n.NodeID, JobID: n.NodeID, Outcome: stepOutcome(n)}
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

func buildCost(nodes []*store.Node, rate float64, rateSource string) controllerwire.ReceiptCost {
	c := controllerwire.ReceiptCost{Currency: "USD", RateSource: rateSource, Settled: false}
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

func computeReceiptSHA(r controllerwire.Receipt) string {
	r.ReceiptSHA = ""
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
