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
type PipelineProfile struct {
	Pipeline        string        `json:"pipeline"`
	NodeID          string        `json:"node_id"`
	P50Duration     time.Duration `json:"p50_duration_ns"`
	P99Duration     time.Duration `json:"p99_duration_ns"`
	PeakCores       float64       `json:"peak_cores"`
	PeakMemoryBytes int64         `json:"peak_memory_bytes"`
	SampleCount     int           `json:"sample_count"`
	UpdatedAt       time.Time     `json:"updated_at"`
	// PinnedCores and PinnedMemoryBytes record the explicit .Resources()
	// pin last seen for this pipeline, or zero when it declared none. They
	// let a reader recompute pin drift against the measured peaks without
	// the pipeline's source. Meaningful only on the rollup row.
	PinnedCores       float64 `json:"pinned_cores,omitempty"`
	PinnedMemoryBytes int64   `json:"pinned_memory_bytes,omitempty"`
}

// ProfileObservation is one run's contribution to a profile: how long the
// work took and the peak host resources it drew.
type ProfileObservation struct {
	Duration        time.Duration
	PeakCores       float64
	PeakMemoryBytes int64
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
    (pipeline, node_id, p50_duration_ms, p99_duration_ms, peak_cores, peak_memory_bytes, sample_count, updated_at, samples_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (pipeline, node_id) DO UPDATE SET
    p50_duration_ms   = excluded.p50_duration_ms,
    p99_duration_ms   = excluded.p99_duration_ms,
    peak_cores        = excluded.peak_cores,
    peak_memory_bytes = excluded.peak_memory_bytes,
    sample_count      = excluded.sample_count,
    updated_at        = excluded.updated_at,
    samples_json      = excluded.samples_json`,
			pipeline, nodeID,
			prof.P50Duration.Milliseconds(), prof.P99Duration.Milliseconds(),
			prof.PeakCores, prof.PeakMemoryBytes, len(window),
			time.Now().UnixNano(), raw)
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

// GetPipelineProfile returns the (pipeline, node) profile, or nil when no
// runs have been measured for it yet.
func (s *Store) GetPipelineProfile(ctx context.Context, pipeline, nodeID string) (*PipelineProfile, error) {
	row := s.queryRow(ctx, `
SELECT p50_duration_ms, p99_duration_ms, peak_cores, peak_memory_bytes, sample_count, updated_at, pinned_cores, pinned_memory_bytes
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
SELECT pipeline, node_id, p50_duration_ms, p99_duration_ms, peak_cores, peak_memory_bytes, sample_count, updated_at, pinned_cores, pinned_memory_bytes
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
		var (
			p, n         string
			p50, p99     int64
			cores        float64
			mem          int64
			count        int
			updatedNanos int64
			pinCores     float64
			pinMem       int64
		)
		if err := rows.Scan(&p, &n, &p50, &p99, &cores, &mem, &count, &updatedNanos, &pinCores, &pinMem); err != nil {
			return nil, err
		}
		out = append(out, PipelineProfile{
			Pipeline:          p,
			NodeID:            n,
			P50Duration:       time.Duration(p50) * time.Millisecond,
			P99Duration:       time.Duration(p99) * time.Millisecond,
			PeakCores:         cores,
			PeakMemoryBytes:   mem,
			SampleCount:       count,
			UpdatedAt:         time.Unix(0, updatedNanos),
			PinnedCores:       pinCores,
			PinnedMemoryBytes: pinMem,
		})
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
	var (
		p50, p99     int64
		cores        float64
		mem          int64
		count        int
		updatedNanos int64
		pinCores     float64
		pinMem       int64
	)
	if err := row.Scan(&p50, &p99, &cores, &mem, &count, &updatedNanos, &pinCores, &pinMem); err != nil {
		return nil, err
	}
	return &PipelineProfile{
		Pipeline:          pipeline,
		NodeID:            nodeID,
		P50Duration:       time.Duration(p50) * time.Millisecond,
		P99Duration:       time.Duration(p99) * time.Millisecond,
		PeakCores:         cores,
		PeakMemoryBytes:   mem,
		SampleCount:       count,
		UpdatedAt:         time.Unix(0, updatedNanos),
		PinnedCores:       pinCores,
		PinnedMemoryBytes: pinMem,
	}, nil
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
