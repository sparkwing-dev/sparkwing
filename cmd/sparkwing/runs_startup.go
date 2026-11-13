package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/opsview"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

type startupObservation struct {
	Scope            string           `json:"scope"`
	JobID            string           `json:"job_id,omitempty"`
	Generation       int64            `json:"generation"`
	SourceGeneration int64            `json:"source_generation,omitempty"`
	Attempt          int64            `json:"attempt,omitempty"`
	PodUID           string           `json:"pod_uid,omitempty"`
	Stage            string           `json:"stage"`
	At               *time.Time       `json:"at"`
	StartedAt        *time.Time       `json:"started_at,omitempty"`
	DurationMS       *int64           `json:"duration_ms,omitempty"`
	Success          *bool            `json:"success,omitempty"`
	Cache            *engine.SDKCache `json:"cache,omitempty"`
}

func readStartup(ctx context.Context, logs controllerwire.LogReader, run controllerwire.RunDetail, events []controllerwire.LogRecord) ([]startupObservation, error) {
	if events == nil {
		var err error
		events, err = readRunEvents(ctx, logs, run.ID)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return []startupObservation{{Scope: "run", Stage: "startup_unavailable"}}, nil
		}
	}
	var observations []startupObservation
	for _, event := range events {
		if event.Event == "owner_claimed" {
			var receipt struct {
				Kind             string    `json:"kind"`
				Generation       int64     `json:"generation"`
				SourceGeneration int64     `json:"source_generation"`
				ClaimedAt        time.Time `json:"claimed_at"`
			}
			if json.Unmarshal([]byte(event.Line), &receipt) == nil && !receipt.ClaimedAt.IsZero() {
				observations = append(observations, startupObservation{Scope: receipt.Kind, JobID: event.JobID, Generation: receipt.Generation, SourceGeneration: receipt.SourceGeneration, Stage: "claim", At: &receipt.ClaimedAt})
			}
		}
		if event.Event != "cloud_startup" {
			continue
		}
		var receipt struct {
			Award capability.LeaseRef `json:"award"`
			Unit  struct {
				NodeID           string
				SourceGeneration int64
			} `json:"unit"`
			Startup capability.PodStartup `json:"startup"`
		}
		if json.Unmarshal([]byte(event.Line), &receipt) != nil {
			continue
		}
		base := startupObservation{Scope: string(receipt.Award.Kind), JobID: receipt.Unit.NodeID, Generation: receipt.Award.Generation, SourceGeneration: receipt.Unit.SourceGeneration, PodUID: receipt.Startup.PodUID}
		for _, boundary := range []struct {
			stage string
			at    *time.Time
		}{
			{"job_created", receipt.Startup.JobCreatedAt}, {"job_activated", receipt.Startup.JobActivatedAt}, {"pod_created", receipt.Startup.PodCreatedAt}, {"pod_scheduled", receipt.Startup.ScheduledAt}, {"image_pull_started", receipt.Startup.ImagePullStartedAt}, {"image_pulled", receipt.Startup.ImagePulledAt}, {"container_started", receipt.Startup.ContainerStartedAt},
		} {
			observation := base
			observation.Stage, observation.At = boundary.stage, boundary.at
			observations = append(observations, observation)
		}
	}
	if len(run.Logs) > 0 {
		query := controllerwire.LogQuery{NoEvents: true, Head: new(128), Grep: `"event":"preparation"`}
		seen := map[string]bool{}
		for {
			page, err := logs.ReadLogs(ctx, run.ID, query)
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				return append(observations, startupObservation{Scope: "run", Stage: "preparation_unavailable"}), nil
			}
			for _, record := range page.Records {
				if record.RunID != run.ID || record.Subject == nil {
					continue
				}
				var frame struct {
					Event string        `json:"event"`
					Attrs source.Timing `json:"attrs"`
				}
				if json.Unmarshal([]byte(record.Line), &frame) != nil || frame.Event != "preparation" || frame.Attrs.StartedAt.IsZero() || frame.Attrs.DurationMS < 0 || frame.Attrs.DurationMS > int64((24*time.Hour)/time.Millisecond) {
					continue
				}
				if !slices.Contains([]string{"restore", "checkout", "engine", "sdk_install", "publish", "describe", "plan"}, frame.Attrs.Stage) {
					continue
				}
				timing := frame.Attrs
				if timing.Cache != nil && !timing.Cache.Valid() {
					timing.Cache = nil
				}
				at := timing.StartedAt.Add(time.Duration(timing.DurationMS) * time.Millisecond)
				observation := startupObservation{Stage: timing.Stage, At: &at, StartedAt: &timing.StartedAt, DurationMS: &timing.DurationMS, Success: &timing.Success, Cache: timing.Cache}
				if unit := record.Subject.Unit; unit != nil {
					observation.Scope, observation.Generation = string(unit.Kind), unit.Generation
					if unit.Source != nil {
						observation.JobID, observation.SourceGeneration = unit.Source.NodeID, unit.Source.Generation
					}
				} else if attempt := record.Subject.Attempt; attempt != nil {
					observation.Scope, observation.JobID, observation.Generation, observation.Attempt = "node", attempt.NodeID, attempt.Generation, attempt.Attempt
				} else {
					continue
				}
				observations = append(observations, observation)
				if timing.Success && (timing.Stage == "checkout" || timing.Stage == "restore" && timing.Reused) {
					milestone := observation
					milestone.Stage, milestone.StartedAt, milestone.DurationMS, milestone.Cache = "source_acquired", nil, nil, nil
					observations = append(observations, milestone)
				}
				if timing.Success && (timing.Stage == "sdk_install" || timing.Stage == "restore" && timing.Reused) {
					milestone := observation
					milestone.Stage, milestone.StartedAt, milestone.DurationMS, milestone.Cache = "sdk_ready", nil, nil, nil
					observations = append(observations, milestone)
				}
			}
			if !page.Truncated {
				break
			}
			if page.NextCursor == "" || page.NextCursor == query.Cursor || seen[page.NextCursor] {
				return nil, invalidObservation("startup log pagination did not advance")
			}
			seen[page.NextCursor] = true
			query.Cursor = page.NextCursor
		}
	}
	// safety: launcher polls can carry successively more boundaries for one Pod; keep only its latest known values.
	var result []startupObservation
	indexes := map[string]int{}
	for _, observation := range observations {
		key := fmt.Sprintf("%s/%s/%d/%d/%d/%s/%s", observation.Scope, observation.JobID, observation.Generation, observation.SourceGeneration, observation.Attempt, observation.PodUID, observation.Stage)
		if index, ok := indexes[key]; ok {
			if observation.At != nil {
				result[index] = observation
			}
		} else {
			indexes[key] = len(result)
			result = append(result, observation)
		}
	}
	slices.SortStableFunc(result, func(a, b startupObservation) int {
		if a.At == nil {
			if b.At == nil {
				return 0
			}
			return 1
		}
		if b.At == nil {
			return -1
		}
		return a.At.Compare(*b.At)
	})
	return result, nil
}

func writeStartup(out io.Writer, observations []startupObservation) error {
	if len(observations) == 0 {
		return nil
	}
	if _, err := fmt.Fprintln(out, "\nStartup (recorded boundaries; missing timestamps are unknown):"); err != nil {
		return err
	}
	for _, observation := range observations[:min(len(observations), 50)] {
		duration, cache, fetched := "-", "unknown", "unknown"
		if observation.DurationMS != nil {
			duration = (time.Duration(*observation.DurationMS) * time.Millisecond).String()
		}
		if observation.Cache != nil {
			cache = observation.Cache.Outcome
			if observation.Cache.BytesFetched != nil {
				fetched = strconv.FormatInt(*observation.Cache.BytesFetched, 10)
			}
		}
		if _, err := fmt.Fprintf(out, "%s %s generation=%d attempt=%d %s\t%s\t%s\tcache=%s bytes_fetched=%s\n", opsview.DisplayText(observation.Scope), opsview.DisplayText(observation.JobID), observation.Generation, observation.Attempt, observation.Stage, displayTime(observation.At), duration, opsview.DisplayText(cache), fetched); err != nil {
			return err
		}
	}
	if len(observations) > 50 {
		_, err := fmt.Fprintf(out, "%d more startup observations; use -o json for all records.\n", len(observations)-50)
		return err
	}
	return nil
}
