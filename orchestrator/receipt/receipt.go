// Package receipt computes the per-run audit + cost summary
// surfaced by `sparkwing runs receipt` and GET /api/v1/runs/{id}/receipt.
//
// The receipt is recomputed from runs+nodes on demand (IMP-016
// Storage approach). Only the small queryable fields (receipt_sha,
// cost_*) live on the runs row; the full JSON is regenerated each
// read so the receipt always reflects the current store contents.
package receipt

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"

	"github.com/sparkwing-dev/sparkwing/v2/orchestrator/store"
)

// Receipt is the per-run audit + cost artifact. JSON shape is the
// public contract documented in IMP-016 / docs.
type Receipt struct {
	RunID      string     `json:"run_id"`
	Pipeline   string     `json:"pipeline"`
	GitSHA     string     `json:"git_sha"`
	Status     string     `json:"status"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	DurationMS int64      `json:"duration_ms"`
	Identity   Identity   `json:"identity"`
	Steps      []Step     `json:"steps"`
	Cost       Cost       `json:"cost"`
	ReceiptSHA string     `json:"receipt_sha"`
}

// Identity carries the four hashes that make two runs comparable
// without rerunning either.
type Identity struct {
	PipelineVersionHash string            `json:"pipeline_version_hash"`
	InputsHash          string            `json:"inputs_hash"`
	PlanHash            string            `json:"plan_hash"`
	OutputsHash         map[string]string `json:"outputs_hash"`
}

// Step is one row in the per-step observability section. Skipped
// nodes appear with outcome=skipped and an optional skip_reason.
type Step struct {
	ID         string `json:"id"`
	NodeID     string `json:"node_id"`
	DurationMS int64  `json:"duration_ms"`
	Outcome    string `json:"outcome"`
	SkipReason string `json:"skip_reason,omitempty"`
}

// Cost is the runner-time × profile-rate compute cost. Cloud-billing
// reconciliation (IMP-018) flips Settled to true.
type Cost struct {
	Currency     string `json:"currency"`
	ComputeCents int64  `json:"compute_cents"`
	RateSource   string `json:"rate_source"`
	Settled      bool   `json:"settled"`
}

// BuildReceipt assembles a Receipt from store rows. Pure: no I/O,
// deterministic, safe to call repeatedly. rate is USD per runner
// hour (0 = unconfigured -> compute_cents:0). rateSource is a
// human-readable provenance string (e.g. "profile:prod
// (cost_per_runner_hour=$0.05)") shown in the receipt.
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
		InputsHash:          hashCanonical(run.Args),
		PlanHash:            planTopologyHash(nodes),
		OutputsHash:         outputsHashes(nodes),
	}
	return id
}

// planTopologyHash hashes node IDs + their dep edges. The node body
// (Status / Outcome / timing / output) is excluded so two runs with
// identical DAG shape produce the same plan_hash regardless of how
// they ran.
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

// outputsHashes returns a per-node sha of the typed Output JSON.
// Skipped / never-ran nodes contribute the empty hash, kept out of
// the map so the receipt isn't padded with nulls.
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

// stepOutcome normalizes the node's recorded outcome so the receipt
// always uses one of {success,failed,skipped,cancelled}. An empty
// outcome on a non-terminal node is reported as "running".
func stepOutcome(n *store.Node) string {
	if n.Outcome != "" {
		return n.Outcome
	}
	if n.Status == "skipped" {
		return "skipped"
	}
	return n.Status
}

// buildCost sums runner-time across nodes that actually ran (have
// both started_at and finished_at, regardless of outcome) and
// multiplies by the profile rate. Skipped/cancelled nodes do not
// have a started_at and therefore contribute zero.
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
		// Skip step outcomes that didn't consume runner time.
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

// computeReceiptSHA hashes the canonical encoding of r with
// ReceiptSHA blanked, so the field can certify "this is the receipt
// I computed for this run state."
func computeReceiptSHA(r Receipt) string {
	r.ReceiptSHA = ""
	return hashCanonical(r)
}

// hashCanonical marshals v with sorted map keys (the encoding/json
// default for map[string]X) and returns sha256:<hex>. Slices retain
// caller order; callers that need set-style stability sort first.
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
