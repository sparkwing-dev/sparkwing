package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"time"
)

// CostSource names how a run's admission cost was resolved. It is
// recorded on the lease and echoed in the queue view so an operator can
// see whether a charge came from an explicit pin, from measurement, or
// from the conservative cold-start default.
type CostSource string

const (
	// CostSourcePin is an explicit .Resources() pin: authoritative, used
	// verbatim.
	CostSourcePin CostSource = "pin"
	// CostSourceMeasured is a measured profile with enough samples to
	// trust.
	CostSourceMeasured CostSource = "measured"
	// CostSourceDefault is the cold-start default charged to an unknown
	// pipeline's first runs.
	CostSourceDefault CostSource = "default"
)

// profileWindow bounds how many recent run observations back a profile's
// percentiles. Old observations age out so the profile tracks the
// pipeline as its cost drifts.
const profileWindow = 50

// PipelineProfile is the measured resource fingerprint of a
// (pipeline, node) pair over a bounded window of recent runs: duration
// percentiles and peak host usage. The pipeline-level rollup used by
// admission and ETA carries the empty node id; per-node rows record where
// the numbers came from.
//
// The resource distribution fields (CPUP50/CPUP95, MemoryP50Bytes/
// MemoryP95Bytes) describe how spiky the pipeline is; they inform the
// reader and never gate admission. Admission charges PeakCores and
// PeakMemoryBytes: under-reserving a spiky pipeline recreates the
// oversubscription the daemon exists to prevent.
type PipelineProfile struct {
	Pipeline        string        `json:"pipeline"`
	NodeID          string        `json:"node_id"`
	P50Duration     time.Duration `json:"p50_duration_ns"`
	P99Duration     time.Duration `json:"p99_duration_ns"`
	PeakCores       float64       `json:"peak_cores"`
	PeakMemoryBytes int64         `json:"peak_memory_bytes"`
	SampleCount     int           `json:"sample_count"`
	// CPUP50 and CPUP95 are the median and 95th-percentile per-run CPU
	// peaks across the window, recomputed from the stored samples on
	// read. Display-only; admission charges PeakCores.
	CPUP50 float64 `json:"cpu_p50,omitempty"`
	CPUP95 float64 `json:"cpu_p95,omitempty"`
	// MemoryP50Bytes and MemoryP95Bytes are the median and
	// 95th-percentile per-run memory peaks across the window. Display-
	// only; admission charges PeakMemoryBytes.
	MemoryP50Bytes int64 `json:"memory_p50_bytes,omitempty"`
	MemoryP95Bytes int64 `json:"memory_p95_bytes,omitempty"`
	// WaitP50 and WaitP99 are the median and 99th-percentile admission
	// queue waits (submit to grant) over a bounded window of recent
	// runs. Meaningful only on the rollup row; run durations exclude
	// this interval.
	WaitP50 time.Duration `json:"wait_p50_ns,omitempty"`
	WaitP99 time.Duration `json:"wait_p99_ns,omitempty"`
	// WaitSampleCount is how many wait observations back WaitP50/WaitP99.
	WaitSampleCount int `json:"wait_sample_count,omitempty"`
	// CPUMeasured records whether the sampler that produced these
	// observations could actually measure CPU on this platform. A healthy
	// sampler sets it true even when the peak is a genuine near-zero (a
	// sleep-heavy pipeline), so admission can cost the pipeline at its real
	// tiny cost; a blind sampler leaves it false, keeping the conservative
	// default. Meaningful only on the rollup row.
	CPUMeasured bool      `json:"cpu_measured"`
	UpdatedAt   time.Time `json:"updated_at"`
	// PinnedCores and PinnedMemoryBytes record the explicit .Resources()
	// pin last seen for this pipeline, or zero when it declared none. They
	// let a reader recompute pin drift against the measured peaks without
	// the pipeline's source. Meaningful only on the rollup row.
	PinnedCores       float64 `json:"pinned_cores,omitempty"`
	PinnedMemoryBytes int64   `json:"pinned_memory_bytes,omitempty"`
	// ContendedCount is how many of this pipeline's runs the admission
	// daemon flagged as throttled by host contention. Against SampleCount
	// it gives the pipeline's contended share. Meaningful only on the
	// rollup row.
	ContendedCount int `json:"contended_count,omitempty"`
}

// ProfileObservation is one run's contribution to a profile: how long the
// work took and the peak host resources it drew.
type ProfileObservation struct {
	Duration        time.Duration
	PeakCores       float64
	PeakMemoryBytes int64
	// CPUMeasured reports whether the sampler could measure CPU for this
	// run. It gates whether a near-zero peak is trusted as a real
	// measurement or treated as a blind sampler's uninformative zero.
	CPUMeasured bool
}

// profileSample is one windowed observation as persisted in samples_json.
type profileSample struct {
	D int64   `json:"d"`
	C float64 `json:"c"`
	M int64   `json:"m"`
}

// profileSchemaCurrent stamps the meaning of a profile's stored samples.
// Schema 2 measures rollup duration from admission grant to finish;
// schema 1 (and the older bare-array format) folded admission queue wait
// into the duration, so those samples are discarded on load rather than
// contaminating percentiles until they age out.
const profileSchemaCurrent = 2

// profileWindowDoc is the versioned envelope samples_json holds. The
// bare-array format written before versioning fails to decode into it and
// is treated as an empty, ignorable window.
type profileWindowDoc struct {
	Schema  int             `json:"schema"`
	Samples []profileSample `json:"samples"`
}

// RecordProfileObservation folds one run's observation into the
// (pipeline, node) profile, aging out samples beyond profileWindow and
// recomputing the persisted percentiles. Peaks are the p99 across the
// window so a single outlier run cannot pin the charge forever.
func (s *Store) RecordProfileObservation(ctx context.Context, pipeline, nodeID string, obs ProfileObservation) error {
	window, err := s.loadProfileWindow(ctx, pipeline, nodeID)
	if err != nil {
		return err
	}
	window = append(window, profileSample{D: obs.Duration.Nanoseconds(), C: obs.PeakCores, M: obs.PeakMemoryBytes})
	if len(window) > profileWindow {
		window = window[len(window)-profileWindow:]
	}
	prof := profileFromWindow(window)
	raw, err := json.Marshal(profileWindowDoc{Schema: profileSchemaCurrent, Samples: window})
	if err != nil {
		return err
	}
	return retryOnBusy(func() error {
		_, err := s.exec(ctx, `
INSERT INTO pipeline_profiles
    (pipeline, node_id, p50_duration_ms, p99_duration_ms, peak_cores, peak_memory_bytes, sample_count, cpu_measured, updated_at, samples_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (pipeline, node_id) DO UPDATE SET
    p50_duration_ms   = excluded.p50_duration_ms,
    p99_duration_ms   = excluded.p99_duration_ms,
    peak_cores        = excluded.peak_cores,
    peak_memory_bytes = excluded.peak_memory_bytes,
    sample_count      = excluded.sample_count,
    cpu_measured      = excluded.cpu_measured,
    updated_at        = excluded.updated_at,
    samples_json      = excluded.samples_json`,
			pipeline, nodeID,
			prof.P50Duration.Milliseconds(), prof.P99Duration.Milliseconds(),
			prof.PeakCores, prof.PeakMemoryBytes, len(window),
			boolToInt(obs.CPUMeasured), time.Now().UnixNano(), raw)
		return err
	})
}

func (s *Store) loadProfileWindow(ctx context.Context, pipeline, nodeID string) ([]profileSample, error) {
	var raw []byte
	err := s.queryRow(ctx,
		`SELECT samples_json FROM pipeline_profiles WHERE pipeline = ? AND node_id = ?`,
		pipeline, nodeID).Scan(&raw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var doc profileWindowDoc
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Schema != profileSchemaCurrent {
		return nil, nil
	}
	return doc.Samples, nil
}

// waitWindowDoc is the versioned envelope wait_samples_json holds:
// per-run admission waits in milliseconds, oldest first.
type waitWindowDoc struct {
	Schema  int     `json:"schema"`
	Samples []int64 `json:"samples"`
}

// waitSchemaCurrent stamps the meaning of the stored wait samples.
const waitSchemaCurrent = 1

// RecordWaitObservation folds one run's admission wait (submit to grant)
// into the pipeline's rollup profile, aging out samples beyond
// profileWindow and recomputing the persisted wait percentiles. It is
// observability only: nothing in admission reads the wait columns.
func (s *Store) RecordWaitObservation(ctx context.Context, pipeline string, wait time.Duration) error {
	if pipeline == "" {
		return nil
	}
	if wait < 0 {
		wait = 0
	}
	window, err := s.loadWaitWindow(ctx, pipeline)
	if err != nil {
		return err
	}
	window = append(window, wait.Milliseconds())
	if len(window) > profileWindow {
		window = window[len(window)-profileWindow:]
	}
	xs := make([]float64, len(window))
	for i, ms := range window {
		xs[i] = float64(ms)
	}
	p50 := int64(percentile(xs, 0.50))
	p99 := int64(percentile(xs, 0.99))
	raw, err := json.Marshal(waitWindowDoc{Schema: waitSchemaCurrent, Samples: window})
	if err != nil {
		return err
	}
	return retryOnBusy(func() error {
		_, err := s.exec(ctx, `
INSERT INTO pipeline_profiles
    (pipeline, node_id, p50_duration_ms, p99_duration_ms, peak_cores, peak_memory_bytes, sample_count, cpu_measured, updated_at,
     wait_samples_json, wait_p50_ms, wait_p99_ms, wait_sample_count)
VALUES (?, ?, 0, 0, 0, 0, 0, 0, ?, ?, ?, ?, ?)
ON CONFLICT (pipeline, node_id) DO UPDATE SET
    wait_samples_json = excluded.wait_samples_json,
    wait_p50_ms       = excluded.wait_p50_ms,
    wait_p99_ms       = excluded.wait_p99_ms,
    wait_sample_count = excluded.wait_sample_count`,
			pipeline, "", time.Now().UnixNano(), raw, p50, p99, len(window))
		return err
	})
}

func (s *Store) loadWaitWindow(ctx context.Context, pipeline string) ([]int64, error) {
	var raw []byte
	err := s.queryRow(ctx,
		`SELECT wait_samples_json FROM pipeline_profiles WHERE pipeline = ? AND node_id = ''`,
		pipeline).Scan(&raw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var doc waitWindowDoc
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Schema != waitSchemaCurrent {
		return nil, nil
	}
	return doc.Samples, nil
}

// RecordContention increments a pipeline's tally of runs the admission
// daemon flagged as throttled by host contention. It updates the rollup
// row only and is a no-op when no profile exists yet -- a run cannot be
// flagged contended without the measured baseline that first creates the
// row, so the increment always lands.
func (s *Store) RecordContention(ctx context.Context, pipeline string) error {
	return retryOnBusy(func() error {
		_, err := s.exec(ctx,
			`UPDATE pipeline_profiles SET contended_count = contended_count + 1
			  WHERE pipeline = ? AND node_id = ''`, pipeline)
		return err
	})
}

// SetProfilePin records the explicit .Resources() pin last seen for a
// (pipeline, node), so a reader can judge the pin against the measured
// peaks. It updates only the pin columns of an existing profile row and is
// a no-op when no row exists yet.
func (s *Store) SetProfilePin(ctx context.Context, pipeline, nodeID string, cores float64, memoryBytes int64) error {
	return retryOnBusy(func() error {
		_, err := s.exec(ctx, `
UPDATE pipeline_profiles SET pinned_cores = ?, pinned_memory_bytes = ?
 WHERE pipeline = ? AND node_id = ?`, cores, memoryBytes, pipeline, nodeID)
		return err
	})
}

// UpsertProfilePin records a pin for a (pipeline, node), creating a
// measurement-less row when none exists yet so a cluster runner can report
// what it applied before any run has been profiled. A later
// [Store.RecordProfileObservation] folds measurements into the same row
// without disturbing the pin; a pin-only row (zero samples) never trips a
// drift warning, which needs measured peaks. Unlike [Store.SetProfilePin],
// this is never a no-op.
func (s *Store) UpsertProfilePin(ctx context.Context, pipeline, nodeID string, cores float64, memoryBytes int64) error {
	return retryOnBusy(func() error {
		_, err := s.exec(ctx, `
INSERT INTO pipeline_profiles
    (pipeline, node_id, p50_duration_ms, p99_duration_ms, peak_cores, peak_memory_bytes, sample_count, cpu_measured, updated_at, pinned_cores, pinned_memory_bytes)
VALUES (?, ?, 0, 0, 0, 0, 0, 0, ?, ?, ?)
ON CONFLICT (pipeline, node_id) DO UPDATE SET
    pinned_cores        = excluded.pinned_cores,
    pinned_memory_bytes = excluded.pinned_memory_bytes`,
			pipeline, nodeID, time.Now().UnixNano(), cores, memoryBytes)
		return err
	})
}

// ProfileResetSummary reports what a profile reset removed. RowsDeleted
// counts (pipeline, node) rows removed outright -- those with no pin to
// preserve. RowsCleared counts rows whose learned samples, peaks, and
// waits were zeroed but whose pin was kept, so admission keeps honoring
// the pin while it re-learns. SamplesDropped is the total windowed
// duration samples discarded across both.
type ProfileResetSummary struct {
	Pipelines      []string `json:"pipelines"`
	RowsDeleted    int      `json:"rows_deleted"`
	RowsCleared    int      `json:"rows_cleared"`
	SamplesDropped int      `json:"samples_dropped"`
}

// ResetPipelineProfile clears one pipeline's learned capacity profile --
// its windowed samples, duration and peak percentiles, queue waits, and
// contention tally across the rollup and every node row -- so it re-learns
// from a cold start. An explicit .Resources() pin is preserved: a pinned
// row is zeroed in place rather than deleted, so admission keeps charging
// the pin meanwhile. Resetting a pipeline with no stored profile is a
// no-op that reports zero counts.
func (s *Store) ResetPipelineProfile(ctx context.Context, pipeline string) (ProfileResetSummary, error) {
	return s.resetProfiles(ctx, pipeline)
}

// ResetAllProfiles clears every pipeline's learned capacity profile,
// preserving pins, with the same semantics as [Store.ResetPipelineProfile].
func (s *Store) ResetAllProfiles(ctx context.Context) (ProfileResetSummary, error) {
	return s.resetProfiles(ctx, "")
}

// resetProfiles clears learned profile data. An empty pipeline resets
// every pipeline; a non-empty one restricts the reset to that pipeline.
func (s *Store) resetProfiles(ctx context.Context, pipeline string) (ProfileResetSummary, error) {
	summary := ProfileResetSummary{Pipelines: []string{}}
	andPipeline := ""
	var args []any
	if pipeline != "" {
		andPipeline = " AND pipeline = ?"
		args = append(args, pipeline)
	}
	selWhere := ""
	if pipeline != "" {
		selWhere = " WHERE pipeline = ?"
	}
	rows, err := s.query(ctx, `SELECT pipeline, sample_count, pinned_cores, pinned_memory_bytes FROM pipeline_profiles`+selWhere, args...)
	if err != nil {
		return ProfileResetSummary{}, err
	}
	seen := map[string]bool{}
	for rows.Next() {
		var p string
		var samples int
		var pinCores float64
		var pinMem int64
		if err := rows.Scan(&p, &samples, &pinCores, &pinMem); err != nil {
			_ = rows.Close()
			return ProfileResetSummary{}, err
		}
		summary.SamplesDropped += samples
		if pinCores != 0 || pinMem != 0 {
			summary.RowsCleared++
		} else {
			summary.RowsDeleted++
		}
		if !seen[p] {
			seen[p] = true
			summary.Pipelines = append(summary.Pipelines, p)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return ProfileResetSummary{}, err
	}
	if err := rows.Close(); err != nil {
		return ProfileResetSummary{}, err
	}
	sort.Strings(summary.Pipelines)
	if summary.RowsDeleted == 0 && summary.RowsCleared == 0 {
		return summary, nil
	}
	if err := retryOnBusy(func() error {
		_, derr := s.exec(ctx, `DELETE FROM pipeline_profiles WHERE pinned_cores = 0 AND pinned_memory_bytes = 0`+andPipeline, args...)
		return derr
	}); err != nil {
		return ProfileResetSummary{}, err
	}
	clearArgs := append([]any{time.Now().UnixNano()}, args...)
	if err := retryOnBusy(func() error {
		_, uerr := s.exec(ctx, `
UPDATE pipeline_profiles SET
    p50_duration_ms   = 0,
    p99_duration_ms   = 0,
    peak_cores        = 0,
    peak_memory_bytes = 0,
    sample_count      = 0,
    cpu_measured      = 0,
    samples_json      = NULL,
    wait_samples_json = NULL,
    wait_p50_ms       = 0,
    wait_p99_ms       = 0,
    wait_sample_count = 0,
    contended_count   = 0,
    updated_at        = ?
 WHERE (pinned_cores != 0 OR pinned_memory_bytes != 0)`+andPipeline, clearArgs...)
		return uerr
	}); err != nil {
		return ProfileResetSummary{}, err
	}
	return summary, nil
}

// profileColumns is the shared SELECT column list every profile read
// uses, kept in one place so scanProfile stays in lockstep with it.
const profileColumns = `p50_duration_ms, p99_duration_ms, peak_cores, peak_memory_bytes, sample_count, cpu_measured, updated_at, pinned_cores, pinned_memory_bytes, samples_json, wait_p50_ms, wait_p99_ms, wait_sample_count, contended_count`

// GetPipelineProfile returns the (pipeline, node) profile, or nil when no
// runs have been measured for it yet.
func (s *Store) GetPipelineProfile(ctx context.Context, pipeline, nodeID string) (*PipelineProfile, error) {
	row := s.queryRow(ctx, `
SELECT `+profileColumns+`
  FROM pipeline_profiles WHERE pipeline = ? AND node_id = ?`, pipeline, nodeID)
	prof, err := scanProfile(row, pipeline, nodeID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return prof, nil
}

// ListPipelineProfiles returns every stored profile, node rows and the
// pipeline rollup alike, ordered by pipeline then node id (the empty
// rollup id sorts first). A non-empty pipeline restricts the result.
func (s *Store) ListPipelineProfiles(ctx context.Context, pipeline string) ([]PipelineProfile, error) {
	q := `
SELECT pipeline, node_id, ` + profileColumns + `
  FROM pipeline_profiles`
	var args []any
	if pipeline != "" {
		q += ` WHERE pipeline = ?`
		args = append(args, pipeline)
	}
	rows, err := s.query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []PipelineProfile{}
	for rows.Next() {
		var p, n string
		prof, err := scanProfileInto(rows.Scan, &p, &n)
		if err != nil {
			return nil, err
		}
		prof.Pipeline = p
		prof.NodeID = n
		out = append(out, *prof)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Pipeline != out[j].Pipeline {
			return out[i].Pipeline < out[j].Pipeline
		}
		return out[i].NodeID < out[j].NodeID
	})
	return out, nil
}

func scanProfile(row rowScanner, pipeline, nodeID string) (*PipelineProfile, error) {
	prof, err := scanProfileInto(row.Scan)
	if err != nil {
		return nil, err
	}
	prof.Pipeline = pipeline
	prof.NodeID = nodeID
	return prof, nil
}

// scanProfileInto scans one profileColumns row, prepending any leading
// destinations (the pipeline and node id columns when the query selects
// them), and derives the display-only resource percentiles from the
// stored samples.
func scanProfileInto(scan func(...any) error, lead ...any) (*PipelineProfile, error) {
	var (
		p50, p99         int64
		cores            float64
		mem              int64
		count            int
		cpuMeasured      int
		updatedNanos     int64
		pinCores         float64
		pinMem           int64
		samplesRaw       []byte
		waitP50, waitP99 int64
		waitCount        int
		contendedCount   int
	)
	dests := append(lead,
		&p50, &p99, &cores, &mem, &count, &cpuMeasured, &updatedNanos,
		&pinCores, &pinMem, &samplesRaw, &waitP50, &waitP99, &waitCount, &contendedCount)
	if err := scan(dests...); err != nil {
		return nil, err
	}
	prof := &PipelineProfile{
		P50Duration:       time.Duration(p50) * time.Millisecond,
		P99Duration:       time.Duration(p99) * time.Millisecond,
		PeakCores:         cores,
		PeakMemoryBytes:   mem,
		SampleCount:       count,
		CPUMeasured:       cpuMeasured != 0,
		UpdatedAt:         time.Unix(0, updatedNanos),
		PinnedCores:       pinCores,
		PinnedMemoryBytes: pinMem,
		WaitP50:           time.Duration(waitP50) * time.Millisecond,
		WaitP99:           time.Duration(waitP99) * time.Millisecond,
		WaitSampleCount:   waitCount,
		ContendedCount:    contendedCount,
	}
	annotateResourcePercentiles(prof, samplesRaw)
	return prof, nil
}

// annotateResourcePercentiles fills the display-only CPU and memory
// distribution fields from the persisted sample window. Missing or
// older-format samples leave the fields zero.
func annotateResourcePercentiles(prof *PipelineProfile, raw []byte) {
	if len(raw) == 0 {
		return
	}
	var doc profileWindowDoc
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Schema != profileSchemaCurrent || len(doc.Samples) == 0 {
		return
	}
	cores := make([]float64, len(doc.Samples))
	mems := make([]float64, len(doc.Samples))
	for i, s := range doc.Samples {
		cores[i] = s.C
		mems[i] = float64(s.M)
	}
	prof.CPUP50 = percentile(cores, 0.50)
	prof.CPUP95 = percentile(cores, 0.95)
	prof.MemoryP50Bytes = int64(percentile(mems, 0.50))
	prof.MemoryP95Bytes = int64(percentile(mems, 0.95))
}

func profileFromWindow(window []profileSample) PipelineProfile {
	durations := make([]float64, len(window))
	cores := make([]float64, len(window))
	mems := make([]float64, len(window))
	for i, s := range window {
		durations[i] = float64(s.D)
		cores[i] = s.C
		mems[i] = float64(s.M)
	}
	return PipelineProfile{
		P50Duration:     time.Duration(int64(percentile(durations, 0.50))),
		P99Duration:     time.Duration(int64(percentile(durations, 0.99))),
		PeakCores:       percentile(cores, 0.99),
		PeakMemoryBytes: int64(percentile(mems, 0.99)),
		SampleCount:     len(window),
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// percentile returns the nearest-rank q-percentile (0..1) of xs. An empty
// slice yields zero.
func percentile(xs []float64, q float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sorted := append([]float64(nil), xs...)
	sort.Float64s(sorted)
	rank := int(math.Ceil(q*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}
