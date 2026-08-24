package web

import (
	"context"
	"errors"
	"net/http"
	"runtime"
	"sort"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/capacity"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// The capacity routes publish what admission learned and how it priced it:
// the pipeline table behind `sparkwing runs stats --capacity`, plus, per
// pipeline, the stored sample window with the rank each charge was taken
// from. They exist to be checked against rather than trusted -- a charge no
// sample on screen supports is a bug in the capacity system, and finding it
// by eye is the point.
//
// Live host state is deliberately not served here: the admission daemon
// already answers for it at GET /api/v1/queue (pkg/localws), through the
// same client the CLI's queue view uses, and a second path to the same
// socket could disagree with the first.

// profileReader is the store-backed slice these routes need. Only the
// sqlite backend can answer it -- learned pricing is per machine and lives
// in the local runs store -- so a dashboard in S3 or controller mode reports
// the routes as unavailable rather than inventing an empty table.
type profileReader interface {
	ListPipelineProfiles(ctx context.Context, pipeline string) ([]store.PipelineProfile, error)
	ProfileSamples(ctx context.Context, pipeline, nodeID string) ([]store.ProfileSample, error)
}

// capacityConstants are the resolution knobs the page shows its arithmetic
// against, so a reader recomputing a charge is not left to guess which
// thresholds produced it.
type capacityConstants struct {
	MinSamples          int     `json:"min_samples"`
	ChargePercentile    float64 `json:"charge_percentile"`
	SustainedPercentile float64 `json:"sustained_percentile"`
	WarmStartMultiple   float64 `json:"warm_start_multiple"`
	SafetyMultiple      float64 `json:"safety_multiple"`
	DriftFraction       float64 `json:"drift_fraction"`
	// ColdStartCores is what an unmeasured pipeline is charged on this
	// machine, resolved rather than recomputed so the figure cannot drift
	// from the one admission uses.
	ColdStartCores float64 `json:"cold_start_cores"`
}

// capacityCharge is one resolved admission price with its provenance.
type capacityCharge struct {
	Cores       float64 `json:"cores"`
	MemoryBytes int64   `json:"memory_bytes"`
	// Source is the [store.CostSource] admission resolved, and Rationale its
	// canonical phrasing -- the same wording the daemon puts on a blocked
	// waiter, so the panel and the queue never explain one charge two ways.
	Source    string `json:"source"`
	Rationale string `json:"rationale,omitempty"`
	// CoresBasis names the figure the core charge was taken from:
	// "sustained_p95", "peak_p95" when a profile predates sustained
	// figures, "pin", "floor", "prev_charge", or "cold_start".
	CoresBasis string `json:"cores_basis"`
	// FloorApplied reports that the resolved charge is the small measured
	// core floor rather than the basis figure, which is otherwise invisible
	// on a pipeline the sampler saw drawing near-zero CPU.
	FloorApplied bool `json:"floor_applied,omitempty"`
}

// capacityProfile is one pipeline's row in the priced table: the stored
// rollup, the charge it resolves to, and any pin drift.
type capacityProfile struct {
	Pipeline string         `json:"pipeline"`
	Charge   capacityCharge `json:"charge"`

	SampleCount     int     `json:"sample_count"`
	PeakCores       float64 `json:"peak_cores"`
	SustainedCores  float64 `json:"sustained_cores"`
	PeakMemoryBytes int64   `json:"peak_memory_bytes"`
	CPUP50          float64 `json:"cpu_p50"`
	CPUP95          float64 `json:"cpu_p95"`
	MemoryP50Bytes  int64   `json:"memory_p50_bytes"`
	MemoryP95Bytes  int64   `json:"memory_p95_bytes"`
	CPUMeasured     bool    `json:"cpu_measured"`

	P50DurationMS int64 `json:"p50_duration_ms"`
	P99DurationMS int64 `json:"p99_duration_ms"`
	WaitP50MS     int64 `json:"wait_p50_ms,omitempty"`
	WaitP99MS     int64 `json:"wait_p99_ms,omitempty"`

	FloorCores       float64 `json:"floor_cores,omitempty"`
	FloorMemoryBytes int64   `json:"floor_memory_bytes,omitempty"`

	PinnedCores       float64 `json:"pinned_cores,omitempty"`
	PinnedMemoryBytes int64   `json:"pinned_memory_bytes,omitempty"`
	Drift             string  `json:"drift,omitempty"`
	DriftClass        string  `json:"drift_class,omitempty"`

	ContendedCount int    `json:"contended_count,omitempty"`
	PlanHash       string `json:"plan_hash,omitempty"`
	UpdatedAtMS    int64  `json:"updated_at_ms,omitempty"`
	NodeCount      int    `json:"node_count,omitempty"`
}

// capacityNode is one node row under a pipeline: where the rollup's numbers
// came from inside the DAG.
type capacityNode struct {
	NodeID          string  `json:"node_id"`
	SampleCount     int     `json:"sample_count"`
	PeakCores       float64 `json:"peak_cores"`
	SustainedCores  float64 `json:"sustained_cores"`
	PeakMemoryBytes int64   `json:"peak_memory_bytes"`
	P50DurationMS   int64   `json:"p50_duration_ms"`
	P99DurationMS   int64   `json:"p99_duration_ms"`
}

// chargeStep is one rung of the resolution order, evaluated whether or not
// it was taken. Showing the rungs that lost, with the figures they would
// have charged, is what turns a price into an argument a reader can check.
type chargeStep struct {
	Step        string  `json:"step"`
	Label       string  `json:"label"`
	Cores       float64 `json:"cores,omitempty"`
	MemoryBytes int64   `json:"memory_bytes,omitempty"`
	Eligible    bool    `json:"eligible"`
	Applied     bool    `json:"applied"`
	Detail      string  `json:"detail,omitempty"`
}

// rankSelection points at the one sample a percentile charge was taken
// from, and reports whether recomputing from the window reproduces the
// stored figure. A false Matches is the interesting case: the charge and
// the evidence behind it have diverged.
type rankSelection struct {
	Field      string  `json:"field"`
	Percentile float64 `json:"percentile"`
	// Rank is the 1-based nearest-rank position within the sorted window,
	// and Count its size, so "rank 12 of 12" reads as the arithmetic it is.
	Rank  int `json:"rank"`
	Count int `json:"count"`
	// Index is the position of the selected sample in the window as
	// displayed (oldest first), or -1 when the window is empty.
	Index      int     `json:"index"`
	Value      float64 `json:"value"`
	Stored     float64 `json:"stored"`
	Matches    bool    `json:"matches"`
	Unmeasured bool    `json:"unmeasured,omitempty"`
}

type capacitySample struct {
	Index           int     `json:"index"`
	DurationMS      int64   `json:"duration_ms"`
	PeakCores       float64 `json:"peak_cores"`
	SustainedCores  float64 `json:"sustained_cores"`
	PeakMemoryBytes int64   `json:"peak_memory_bytes"`
}

type capacityProfilesPayload struct {
	MachineCores int               `json:"machine_cores"`
	Constants    capacityConstants `json:"constants"`
	Profiles     []capacityProfile `json:"profiles"`
	GeneratedMS  int64             `json:"generated_at_ms"`
}

// capacitySelection groups the ranks every stored percentile was taken at,
// one per figure the table shows.
type capacitySelection struct {
	Cores       rankSelection `json:"cores"`
	Memory      rankSelection `json:"memory"`
	DurationP50 rankSelection `json:"duration_p50"`
	DurationP99 rankSelection `json:"duration_p99"`
}

// capacityExplainPayload is the derivation behind one pipeline's charge:
// the priced row, the window it was priced from with the selected ranks,
// the resolution order, and the node rows underneath.
type capacityExplainPayload struct {
	MachineCores int               `json:"machine_cores"`
	Constants    capacityConstants `json:"constants"`
	Profile      capacityProfile   `json:"profile"`
	Chain        []chargeStep      `json:"chain"`
	Samples      []capacitySample  `json:"samples"`
	Selections   capacitySelection `json:"selections"`
	Nodes        []capacityNode    `json:"nodes"`
	// CeilingNote names the cap the daemon applies at admission time, which
	// this view cannot see: the charge here is what measurement resolves,
	// not necessarily what a run was granted on a busy machine.
	CeilingNote string `json:"ceiling_note"`
	GeneratedMS int64  `json:"generated_at_ms"`
}

// capacityProfilesHandler serves GET /api/v1/capacity/profiles: every
// measured pipeline with the charge it resolves to, the live page behind
// `sparkwing runs stats --capacity`. Node rows are folded into their
// pipeline; the explain route serves them.
func capacityProfilesHandler(b backend.Backend) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reader, ok := profileReaderFor(b)
		if !ok {
			writeErr(w, http.StatusNotImplemented, errUnsupportedProfiles)
			return
		}
		rows, err := reader.ListPipelineProfiles(r.Context(), "")
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		numCPU := runtime.NumCPU()
		grouped := groupProfiles(rows)
		profiles := make([]capacityProfile, 0, len(grouped))
		for _, g := range grouped {
			profiles = append(profiles, newCapacityProfile(g.rollup, len(g.nodes), numCPU))
		}
		writeJSON(w, http.StatusOK, capacityProfilesPayload{
			MachineCores: numCPU,
			Constants:    constantsFor(numCPU),
			Profiles:     profiles,
			GeneratedMS:  time.Now().UnixMilli(),
		})
	}
}

// capacityExplainHandler serves GET /api/v1/capacity/profiles/explain?
// pipeline=KEY: the same row, plus the stored sample window, the rank each
// charge was taken at, and the resolution order that produced it. The
// pipeline key arrives as a query parameter because profile keys are
// "repo/pipeline" and a path segment cannot carry the slash.
func capacityExplainHandler(b backend.Backend) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reader, ok := profileReaderFor(b)
		if !ok {
			writeErr(w, http.StatusNotImplemented, errUnsupportedProfiles)
			return
		}
		pipeline := r.URL.Query().Get("pipeline")
		if pipeline == "" {
			writeErr(w, http.StatusBadRequest, errors.New("pipeline is required"))
			return
		}
		rows, err := reader.ListPipelineProfiles(r.Context(), pipeline)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if len(rows) == 0 {
			writeErr(w, http.StatusNotFound, errors.New("no measured profile for "+pipeline))
			return
		}
		samples, err := reader.ProfileSamples(r.Context(), pipeline, "")
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}

		numCPU := runtime.NumCPU()
		g := groupProfiles(rows)[0]
		writeJSON(w, http.StatusOK, capacityExplainPayload{
			MachineCores: numCPU,
			Constants:    constantsFor(numCPU),
			Profile:      newCapacityProfile(g.rollup, len(g.nodes), numCPU),
			Chain:        chargeChain(g.rollup, numCPU),
			Samples:      sampleRows(samples),
			Selections:   selectionsFor(g.rollup, samples),
			Nodes:        nodeRows(g.nodes),
			CeilingNote: "The daemon caps a measured or measuring charge at the machine's grantable " +
				"headroom when it admits a run, so a run on a busy host can hold less than the charge above.",
			GeneratedMS: time.Now().UnixMilli(),
		})
	}
}

var errUnsupportedProfiles = errors.New(
	"learned capacity profiles live in the local runs store; this dashboard is not reading one")

// profileReaderFor unwraps the sqlite store behind the dashboard backend.
// It mirrors how the local CLI reaches store-only helpers rather than
// widening the Backend interface for impls that have no equivalent.
func profileReaderFor(b backend.Backend) (profileReader, bool) {
	sb, ok := b.(interface{ Store() *store.Store })
	if !ok || sb.Store() == nil {
		return nil, false
	}
	return sb.Store(), true
}

// profileGroup is one pipeline's rollup row with the node rows beneath it.
type profileGroup struct {
	rollup store.PipelineProfile
	nodes  []store.PipelineProfile
}

// groupProfiles folds the flat profile rows into per-pipeline groups,
// splitting the rollup (empty node id) from its node rows. It mirrors the
// CLI's grouping so the two views cannot disagree about which row prices a
// pipeline.
func groupProfiles(rows []store.PipelineProfile) []profileGroup {
	byPipeline := map[string]*profileGroup{}
	order := []string{}
	for _, p := range rows {
		g, ok := byPipeline[p.Pipeline]
		if !ok {
			g = &profileGroup{rollup: store.PipelineProfile{Pipeline: p.Pipeline}}
			byPipeline[p.Pipeline] = g
			order = append(order, p.Pipeline)
		}
		if p.NodeID == "" {
			g.rollup = p
			continue
		}
		g.nodes = append(g.nodes, p)
	}
	out := make([]profileGroup, 0, len(order))
	for _, name := range order {
		g := byPipeline[name]
		sort.Slice(g.nodes, func(i, j int) bool { return g.nodes[i].NodeID < g.nodes[j].NodeID })
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rollup.Pipeline < out[j].rollup.Pipeline })
	return out
}

func constantsFor(numCPU int) capacityConstants {
	return capacityConstants{
		MinSamples:          capacity.MinSamples,
		ChargePercentile:    chargePercentile,
		SustainedPercentile: capacity.SustainedPercentile,
		WarmStartMultiple:   capacity.WarmStartMultiple,
		SafetyMultiple:      capacity.SafetyMultiple,
		DriftFraction:       capacity.DriftFraction,
		ColdStartCores:      coldStartCores(numCPU),
	}
}

// chargePercentile is the rank a stored profile takes across its window for
// both the core and memory charges. It is the display copy of the store's
// own constant; the selections this file reports are computed at this rank
// and checked against the stored figure, so a divergence surfaces as a
// mismatch on the page instead of silently mis-marking a row.
const chargePercentile = 0.95

// coldStartCores asks the resolver what an unmeasured pipeline costs on this
// machine rather than recomputing the fraction, so the page cannot quote a
// cold start the daemon would not charge.
func coldStartCores(numCPU int) float64 {
	return capacity.Resolve(nil, nil, numCPU, "").Cores
}

func newCapacityProfile(rollup store.PipelineProfile, nodeCount, numCPU int) capacityProfile {
	pin := pinOf(rollup)
	res := capacity.Resolve(pin, &rollup, numCPU, rollup.PlanHash)
	out := capacityProfile{
		Pipeline:          rollup.Pipeline,
		Charge:            chargeOf(res, rollup),
		SampleCount:       rollup.SampleCount,
		PeakCores:         rollup.PeakCores,
		SustainedCores:    rollup.SustainedCores,
		PeakMemoryBytes:   rollup.PeakMemoryBytes,
		CPUP50:            rollup.CPUP50,
		CPUP95:            rollup.CPUP95,
		MemoryP50Bytes:    rollup.MemoryP50Bytes,
		MemoryP95Bytes:    rollup.MemoryP95Bytes,
		CPUMeasured:       rollup.CPUMeasured,
		P50DurationMS:     rollup.P50Duration.Milliseconds(),
		P99DurationMS:     rollup.P99Duration.Milliseconds(),
		WaitP50MS:         rollup.WaitP50.Milliseconds(),
		WaitP99MS:         rollup.WaitP99.Milliseconds(),
		FloorCores:        rollup.FloorCores,
		FloorMemoryBytes:  rollup.FloorMemoryBytes,
		PinnedCores:       rollup.PinnedCores,
		PinnedMemoryBytes: rollup.PinnedMemoryBytes,
		ContendedCount:    rollup.ContendedCount,
		PlanHash:          rollup.PlanHash,
		NodeCount:         nodeCount,
	}
	if !rollup.UpdatedAt.IsZero() {
		out.UpdatedAtMS = rollup.UpdatedAt.UnixMilli()
	}
	if d := capacity.CheckDrift(pin, &rollup); d != nil {
		out.Drift = d.Message
		out.DriftClass = string(d.Class)
	}
	return out
}

func pinOf(rollup store.PipelineProfile) *capacity.Pin {
	return &capacity.Pin{Cores: rollup.PinnedCores, MemoryBytes: rollup.PinnedMemoryBytes}
}

func chargeOf(res capacity.Resolution, rollup store.PipelineProfile) capacityCharge {
	basis, raw := coresBasis(res.Source, rollup)
	return capacityCharge{
		Cores:        res.Cores,
		MemoryBytes:  res.MemoryBytes,
		Source:       string(res.Source),
		Rationale:    wingwire.CostRationale(wingwire.CostSource(res.Source), rollup.SampleCount),
		CoresBasis:   basis,
		FloorApplied: raw > 0 && res.Cores > raw,
	}
}

// coresBasis names which stored figure the core charge came from, and
// returns that figure so the caller can tell a charge raised to the measured
// core floor from one taken verbatim. The measured case carries the peak
// fallback a profile predating sustained figures still takes.
func coresBasis(source store.CostSource, rollup store.PipelineProfile) (string, float64) {
	switch source {
	case store.CostSourcePin:
		return "pin", rollup.PinnedCores
	case store.CostSourceMeasured:
		if rollup.SustainedCores > 0 {
			return "sustained_p95", rollup.SustainedCores
		}
		return "peak_p95", rollup.PeakCores
	case store.CostSourceFloor:
		return "floor", capacity.SafetyMultiple * rollup.FloorCores
	case store.CostSourceMeasuring:
		return "prev_charge", capacity.WarmStartMultiple * carriedCores(rollup)
	default:
		return "cold_start", 0
	}
}

// carriedCores is what a plan-hash change carried across from the previous
// version: what it was charged, not what it peaked at. It mirrors the
// resolver's own fallback for a predecessor measured before sustained
// figures existed.
func carriedCores(rollup store.PipelineProfile) float64 {
	if rollup.PrevSustainedCores > 0 {
		return rollup.PrevSustainedCores
	}
	return rollup.PrevPeakCores
}

// chargeChain walks the resolution order top to bottom, recording what each
// rung would charge and which one the resolver actually took. Applied is
// keyed off [capacity.Resolve]'s own verdict rather than re-deciding here,
// so the chain cannot claim a step the daemon did not take.
func chargeChain(rollup store.PipelineProfile, numCPU int) []chargeStep {
	pin := pinOf(rollup)
	res := capacity.Resolve(pin, &rollup, numCPU, rollup.PlanHash)
	source := res.Source

	measuredCores := rollup.SustainedCores
	measuredBasis := "sustained p95 across the window"
	if measuredCores == 0 {
		measuredCores = rollup.PeakCores
		measuredBasis = "peak p95; this profile predates sustained figures"
	}
	carried := carriedCores(rollup)

	steps := []chargeStep{
		{
			Step:        "pin",
			Label:       "Explicit .Resources() pin",
			Cores:       rollup.PinnedCores,
			MemoryBytes: rollup.PinnedMemoryBytes,
			Eligible:    !pin.Empty(),
			Applied:     source == store.CostSourcePin,
			Detail:      "A pin wins verbatim and is policed for drift, never overridden by measurement.",
		},
		{
			Step:        "measured",
			Label:       "Measured profile",
			Cores:       measuredCores,
			MemoryBytes: rollup.PeakMemoryBytes,
			Eligible:    rollup.SampleCount >= capacity.MinSamples && (rollup.PeakCores > 0 || rollup.CPUMeasured),
			Applied:     source == store.CostSourceMeasured,
			Detail:      measuredBasis + "; memory charges the p95 of the per-run peaks.",
		},
		{
			Step:        "prev_charge",
			Label:       "Warm start from the previous version",
			Cores:       capacity.WarmStartMultiple * carried,
			MemoryBytes: int64(capacity.WarmStartMultiple * float64(rollup.PrevPeakMemoryBytes)),
			Eligible:    carried > 0,
			Applied:     source == store.CostSourceMeasuring,
			Detail:      "Charged while a structurally changed version re-measures its own samples.",
		},
		{
			Step:        "floor",
			Label:       "Demand floor from contended runs",
			Cores:       capacity.SafetyMultiple * rollup.FloorCores,
			MemoryBytes: int64(capacity.SafetyMultiple * float64(rollup.FloorMemoryBytes)),
			Eligible:    rollup.FloorCores > 0,
			Applied:     source == store.CostSourceFloor,
			Detail:      "A contended run measured its allocation, not its demand, so it only raises this lower bound.",
		},
		{
			Step:     "cold_start",
			Label:    "Cold-start default",
			Cores:    coldStartCores(numCPU),
			Eligible: true,
			Applied:  source == store.CostSourceDefault,
			Detail:   "Half the machine, so two unknown pipelines cannot hold capacity at once.",
		},
	}
	if basis, raw := coresBasis(source, rollup); raw > 0 && res.Cores > raw {
		steps = append(steps, chargeStep{
			Step:     "measured_floor",
			Label:    "Raised to the minimum measured charge",
			Cores:    res.Cores,
			Eligible: true,
			Applied:  true,
			Detail:   "The " + basis + " figure is below the floor a measured pipeline is still accounted for at.",
		})
	}
	return steps
}

func nodeRows(nodes []store.PipelineProfile) []capacityNode {
	out := make([]capacityNode, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, capacityNode{
			NodeID:          n.NodeID,
			SampleCount:     n.SampleCount,
			PeakCores:       n.PeakCores,
			SustainedCores:  n.SustainedCores,
			PeakMemoryBytes: n.PeakMemoryBytes,
			P50DurationMS:   n.P50Duration.Milliseconds(),
			P99DurationMS:   n.P99Duration.Milliseconds(),
		})
	}
	return out
}

func sampleRows(samples []store.ProfileSample) []capacitySample {
	out := make([]capacitySample, 0, len(samples))
	for i, s := range samples {
		out = append(out, capacitySample{
			Index:           i,
			DurationMS:      s.Duration.Milliseconds(),
			PeakCores:       s.PeakCores,
			SustainedCores:  s.SustainedCores,
			PeakMemoryBytes: s.PeakMemoryBytes,
		})
	}
	return out
}

// selectionsFor recomputes each stored percentile from the window and points
// at the sample it landed on. Stored is carried beside the recomputed value
// so the page can show the two agreeing -- or, when they do not, show that
// the price and its evidence have parted company.
func selectionsFor(rollup store.PipelineProfile, samples []store.ProfileSample) capacitySelection {
	sustained := make([]float64, len(samples))
	peaks := make([]float64, len(samples))
	mems := make([]float64, len(samples))
	durations := make([]float64, len(samples))
	for i, s := range samples {
		sustained[i] = s.SustainedCores
		peaks[i] = s.PeakCores
		mems[i] = float64(s.PeakMemoryBytes)
		durations[i] = float64(s.Duration)
	}

	coresField, coresValues, coresStored := "sustained_cores", sustained, rollup.SustainedCores
	if rollup.SustainedCores == 0 && rollup.PeakCores > 0 {
		coresField, coresValues, coresStored = "peak_cores", peaks, rollup.PeakCores
	}
	return capacitySelection{
		Cores:  selectionAt(coresField, coresValues, chargePercentile, coresStored),
		Memory: selectionAt("peak_memory_bytes", mems, chargePercentile, float64(rollup.PeakMemoryBytes)),
		// Durations are stored rounded to milliseconds while the window keeps
		// nanoseconds, so both sides of the comparison are taken to the stored
		// resolution rather than reporting every profile as mismatched.
		DurationP50: selectionAtMS("duration", durations, 0.50, float64(rollup.P50Duration)),
		DurationP99: selectionAtMS("duration", durations, 0.99, float64(rollup.P99Duration)),
	}
}

func selectionAt(field string, values []float64, q, stored float64) rankSelection {
	sel := rankSelection{
		Field:      field,
		Percentile: q,
		Count:      len(values),
		Index:      -1,
		Stored:     stored,
	}
	if len(values) == 0 {
		sel.Unmeasured = true
		sel.Matches = stored == 0
		return sel
	}
	sel.Index = store.NearestRankIndex(values, q)
	sel.Rank = nearestRankOneBased(len(values), q)
	sel.Value = store.NearestRankPercentile(values, q)
	sel.Matches = sel.Value == stored
	return sel
}

func selectionAtMS(field string, values []float64, q, stored float64) rankSelection {
	sel := selectionAt(field, values, q, stored)
	if len(values) > 0 {
		sel.Matches = int64(sel.Value)/int64(time.Millisecond) == int64(stored)/int64(time.Millisecond)
	}
	return sel
}

// nearestRankOneBased is the rank position the store's percentile arithmetic
// lands on, counted the way a reader counts a sorted list: 1 is smallest. It
// asks the store where an already-sorted run of positions ranks rather than
// restating the ceil arithmetic, which would be free to drift from it.
func nearestRankOneBased(n int, q float64) int {
	positions := make([]float64, n)
	for i := range positions {
		positions[i] = float64(i)
	}
	return store.NearestRankIndex(positions, q) + 1
}
