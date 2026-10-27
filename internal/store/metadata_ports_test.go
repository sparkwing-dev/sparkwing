package store_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func metadataAttempt(t *testing.T, f *storeRecoveryFixture) runspec.AttemptIdentity {
	t.Helper()
	row, err := f.tenant.ReadNode(t.Context(), f.ref)
	if err != nil {
		t.Fatal(err)
	}
	return runspec.AttemptIdentity{Team: string(f.tenant.Team()), RunID: string(f.ref.RunID), NodeID: string(f.ref.NodeID), Generation: int64(row.Generation), Attempt: int64(row.Attempt)}
}

func metadataStep(t *testing.T, f *storeRecoveryFixture, step, state string) {
	t.Helper()
	if err := f.tenant.RecordStep(t.Context(), store.StepReport{Actor: f.holder, Attempt: metadataAttempt(t, f), StepID: step, State: state}); err != nil {
		t.Fatal(err)
	}
}

func metadataText(t *testing.T, f *storeRecoveryFixture, step string) ([]json.RawMessage, string) {
	t.Helper()
	query := `SELECT annotations_json,summary FROM nodes WHERE team=? AND run_id=? AND node_id=?`
	args := []any{f.tenant.Team(), f.ref.RunID, f.ref.NodeID}
	if step != "" {
		query = `SELECT annotations_json,summary FROM node_steps WHERE team=? AND run_id=? AND node_id=? AND attempt=? AND step_id=?`
		args = append(args, metadataAttempt(t, f).Attempt, step)
	}
	var raw []byte
	var summary string
	if err := f.s.DB().QueryRowContext(t.Context(), storetest.Rebind(f.s, query), args...).Scan(&raw, &summary); err != nil {
		t.Fatal(err)
	}
	var entries []json.RawMessage
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &entries); err != nil {
			t.Fatal(err)
		}
	}
	return entries, summary
}

func TestMetadataSummaryOverwriteReplayAndReopen(t *testing.T) {
	f := newStoreRecoveryFixture(t, true)
	metadataStep(t, f, "body", "running")
	for _, step := range []string{"", "body"} {
		for _, text := range []string{"## first", "## second", "## second"} {
			if err := f.tenant.WriteSummary(t.Context(), store.SummaryWrite{Ref: f.ref, Actor: f.holder, StepID: step, Markdown: text}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := f.reopen(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"", "body"} {
		_, summary := metadataText(t, f, step)
		if summary != "## second" {
			t.Fatalf("summary %q = %q", step, summary)
		}
	}
	for _, step := range []string{"missing", "body"} {
		ref := f.ref
		if step == "body" {
			ref.NodeID = "missing"
		}
		if err := f.tenant.WriteSummary(t.Context(), store.SummaryWrite{Ref: ref, Actor: f.holder, StepID: step, Markdown: "refused"}); err == nil {
			t.Fatal("missing target accepted summary")
		}
	}
}

func checkConcurrentMetadata(t *testing.T, f *storeRecoveryFixture) {
	t.Helper()
	metadataStep(t, f, "body", "running")
	const workers, each = 6, 5
	for _, step := range []string{"", "body"} {
		var wg sync.WaitGroup
		for worker := range workers {
			wg.Go(func() {
				for n := range each {
					msg := fmt.Sprintf("%d/%d", worker, n)
					if err := f.tenant.WriteAnnotation(t.Context(), store.AnnotationWrite{Ref: f.ref, Actor: f.holder, StepID: step, Level: "info", Message: msg}); err != nil {
						t.Error(err)
						return
					}
				}
			})
		}
		wg.Wait()
		entries, _ := metadataText(t, f, step)
		seen := map[string]bool{}
		for _, entry := range entries {
			var value struct{ Message string }
			if err := json.Unmarshal(entry, &value); err != nil {
				t.Fatal(err)
			}
			seen[value.Message] = true
		}
		if len(entries) != workers*each || len(seen) != workers*each {
			t.Fatalf("%q annotations = %d entries, %d unique", step, len(entries), len(seen))
		}
	}
	var count int
	if err := f.s.DB().QueryRowContext(t.Context(), storetest.Rebind(f.s, `SELECT annotation_count FROM runs WHERE team=? AND id=?`), f.tenant.Team(), f.ref.RunID).Scan(&count); err != nil || count != 2*workers*each {
		t.Fatalf("annotation count = %d, %v", count, err)
	}
}

func TestMetadataConcurrentAnnotationsKeepEveryEntry(t *testing.T) {
	checkConcurrentMetadata(t, newStoreRecoveryFixture(t, true))
}

func TestPostgresMetadataConcurrentAnnotationsKeepEveryEntry(t *testing.T) {
	checkConcurrentMetadata(t, newStoreRecoveryFixtureAt(t, true, storetest.NewPostgres(t)))
}

func TestMetadataBoundsRefuseWithoutWriting(t *testing.T) {
	for _, step := range []string{"", "body"} {
		t.Run(step, func(t *testing.T) {
			f := newStoreRecoveryFixture(t, true)
			metadataStep(t, f, "body", "running")
			write := store.AnnotationWrite{Ref: f.ref, Actor: f.holder, StepID: step, Message: strings.Repeat("x", int(limits.AnnotationBytes)+1)}
			if err := f.tenant.WriteAnnotation(t.Context(), write); err == nil {
				t.Fatal("oversized annotation accepted")
			}
			if err := f.tenant.WriteSummary(t.Context(), store.SummaryWrite{Ref: f.ref, Actor: f.holder, StepID: step, Markdown: write.Message}); err == nil {
				t.Fatal("oversized summary accepted")
			}
			write.Message = "last"
			if _, err := f.s.DB().ExecContext(t.Context(), storetest.Rebind(f.s, `UPDATE runs SET annotation_count=? WHERE team=? AND id=?`), limits.MaxAnnotations-1, f.tenant.Team(), f.ref.RunID); err != nil {
				t.Fatal(err)
			}
			if err := f.tenant.WriteAnnotation(t.Context(), write); err != nil {
				t.Fatal(err)
			}
			before, _ := metadataText(t, f, step)
			if err := f.tenant.WriteAnnotation(t.Context(), write); err == nil {
				t.Fatal("annotation count exceeded")
			}
			after, _ := metadataText(t, f, step)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("refused annotation changed text")
			}
		})
	}
}

func TestMetadataWritesCannotCrossClaimFencesProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(547, 987))
	f := newStoreRecoveryFixture(t, true)
	metadataStep(t, f, "body", "running")
	attempt := metadataAttempt(t, f)
	for range 32 {
		actor, ref, forged := f.holder, f.ref, attempt
		switch random.IntN(7) {
		case 0:
			actor.Generation += runspec.ClaimGeneration(random.IntN(100) + 1)
		case 1:
			actor.Holder = fmt.Sprintf("foreign-%x", random.Uint64())
		case 2:
			actor.CapabilityPrefix = fmt.Sprintf("foreign-%x", random.Uint64())
		case 3:
			ref.RunID = runspec.RunID(fmt.Sprintf("foreign-%x", random.Uint64()))
			forged.RunID = string(ref.RunID)
		case 4:
			ref.NodeID = runspec.NodeID(fmt.Sprintf("foreign-%x", random.Uint64()))
			forged.NodeID = string(ref.NodeID)
		case 5:
			forged.Team = fmt.Sprintf("foreign-%x", random.Uint64())
			ref.RunID = runspec.RunID(forged.Team)
		case 6:
			actor.Authority.ID = fmt.Sprintf("foreign-%x", random.Uint64())
		}
		for _, step := range []string{"", "body"} {
			if err := f.tenant.WriteAnnotation(t.Context(), store.AnnotationWrite{Ref: ref, Actor: actor, StepID: step, Message: "forged"}); err == nil {
				t.Fatal("forged annotation accepted")
			}
			if err := f.tenant.WriteSummary(t.Context(), store.SummaryWrite{Ref: ref, Actor: actor, StepID: step, Markdown: "forged"}); err == nil {
				t.Fatal("forged summary accepted")
			}
			entries, summary := metadataText(t, f, step)
			if len(entries) != 0 || summary != "" {
				t.Fatal("refused writer changed metadata")
			}
		}
		if err := f.tenant.RecordStep(t.Context(), store.StepReport{Actor: actor, Attempt: forged, StepID: "forged", State: "running"}); err == nil {
			t.Fatal("forged step accepted")
		}
	}
}

func TestMetadataStepStatesAndTerminalSettlement(t *testing.T) {
	for _, outcome := range []runstate.NodeState{runstate.NodeSuccess, runstate.NodeFailed, runstate.NodeCancelled} {
		t.Run(string(outcome), func(t *testing.T) {
			f := newStoreRecoveryFixture(t, true)
			metadataStep(t, f, "done", "passed")
			metadataStep(t, f, "cut-off", "running")
			attempt := metadataAttempt(t, f)
			for _, state := range []string{"running", "failed", "unknown"} {
				err := f.tenant.RecordStep(t.Context(), store.StepReport{Actor: f.holder, Attempt: attempt, StepID: "done", State: state})
				if err == nil {
					t.Fatalf("terminal step accepted %q", state)
				}
			}
			_, err := f.tenant.TransitionNode(t.Context(), store.ReportNode{Ref: f.ref, Actor: f.holder, SourceAttempt: (*runspec.AttemptOrdinal)(&attempt.Attempt), Outcome: outcome, Result: &runstate.NodeResult{Outcome: outcome}})
			if err != nil {
				t.Fatal(err)
			}
			var state string
			var finished *int64
			err = f.s.DB().QueryRowContext(t.Context(), storetest.Rebind(f.s, `SELECT status,finished_at FROM node_steps WHERE team=? AND run_id=? AND node_id=? AND attempt=? AND step_id='cut-off'`), f.tenant.Team(), f.ref.RunID, f.ref.NodeID, attempt.Attempt).Scan(&state, &finished)
			want := map[runstate.NodeState]string{runstate.NodeSuccess: "passed", runstate.NodeFailed: "failed", runstate.NodeCancelled: "cancelled"}[outcome]
			if err != nil || state != want || finished == nil {
				t.Fatalf("unfinished step after job report = %q %v, %v; want %s", state, finished, err, want)
			}
			if err := f.tenant.RecordStep(t.Context(), store.StepReport{Actor: f.holder, Attempt: attempt, StepID: "late", State: "passed"}); err == nil {
				t.Fatal("terminal writer added step")
			}
		})
	}
}

func TestMetadataRunnerClockAheadDoesNotBlockRecovery(t *testing.T) {
	f := newStoreRecoveryFixture(t, true)
	ahead := time.Now().Add(24 * time.Hour)
	if err := f.tenant.RecordStep(t.Context(), store.StepReport{Actor: f.holder, Attempt: metadataAttempt(t, f), StepID: "ahead", State: "running", StartedAt: &ahead}); err != nil {
		t.Fatal(err)
	}
	if err := f.expireHolder(t.Context()); err != nil {
		t.Fatal(err)
	}
	units, err := f.s.AsOperator().ReapUnits(t.Context(), 10)
	if err != nil || len(units) != 1 || !units[0].Requeued {
		t.Fatalf("recover ahead-clock runner: %+v, %v", units, err)
	}
	steps, err := f.tenant.ListNodeSteps(t.Context(), string(f.ref.RunID))
	if err != nil || len(steps) != 1 || steps[0].Status != "failed" || steps[0].FinishedAt == nil {
		t.Fatalf("recovered steps = %+v, %v", steps, err)
	}
	var started, finished, sourceStarted int64
	err = f.s.DB().QueryRowContext(t.Context(), storetest.Rebind(f.s, `SELECT started_at,finished_at,source_started_at FROM node_steps WHERE team=? AND run_id=? AND step_id='ahead'`), f.tenant.Team(), f.ref.RunID).Scan(&started, &finished, &sourceStarted)
	if err != nil || finished < started || finished >= sourceStarted || sourceStarted != ahead.UnixNano() {
		t.Fatalf("authoritative times %d..%d, source start %d: %v", started, finished, sourceStarted, err)
	}
}

func TestMetadataMissingStepDoesNotCreatePlaceholder(t *testing.T) {
	f := newStoreRecoveryFixture(t, true)
	for _, write := range []func() error{
		func() error {
			return f.tenant.WriteAnnotation(t.Context(), store.AnnotationWrite{Ref: f.ref, Actor: f.holder, StepID: "missing", Message: "early"})
		},
		func() error {
			return f.tenant.WriteSummary(t.Context(), store.SummaryWrite{Ref: f.ref, Actor: f.holder, StepID: "missing", Markdown: "early"})
		},
	} {
		if err := write(); err == nil {
			t.Fatal("missing step accepted metadata")
		}
	}
	if err := f.tenant.RecordStep(t.Context(), store.StepReport{Actor: f.holder, Attempt: metadataAttempt(t, f), StepID: "missing", State: "skipped"}); err != nil {
		t.Fatal(err)
	}
	err := f.tenant.RecordStep(t.Context(), store.StepReport{Actor: f.holder, Attempt: metadataAttempt(t, f), StepID: "missing", State: "running"})
	var refused *apierr.Error
	if !errors.As(err, &refused) || refused.Code != apierr.IllegalTransition {
		t.Fatalf("reopened skipped step: %v", err)
	}
}

func TestMetadataRetryClosesOnlyTheEndingAttempt(t *testing.T) {
	f := newStoreRecoveryFixture(t, true)
	metadataStep(t, f, "body", "running")
	first := metadataAttempt(t, f)
	retry := store.RetryNode{Ref: f.ref, Actor: f.holder, SourceAttempt: (*runspec.AttemptOrdinal)(&first.Attempt), Result: &runstate.NodeResult{Outcome: runstate.NodeFailed}}
	if _, err := f.tenant.TransitionNode(t.Context(), retry); err != nil {
		t.Fatal(err)
	}
	metadataStep(t, f, "body", "running")
	second := metadataAttempt(t, f)
	if second.Attempt != first.Attempt+1 {
		t.Fatal("retry did not advance attempt")
	}
	if _, err := f.tenant.TransitionNode(t.Context(), store.ReportNode{Ref: f.ref, Actor: f.holder, SourceAttempt: (*runspec.AttemptOrdinal)(&second.Attempt), Outcome: runstate.NodeSuccess, Result: &runstate.NodeResult{Outcome: runstate.NodeSuccess}}); err != nil {
		t.Fatal(err)
	}
	rows, err := f.s.DB().QueryContext(t.Context(), storetest.Rebind(f.s, `SELECT attempt,status,finished_at FROM node_steps WHERE team=? AND run_id=? AND node_id=? ORDER BY attempt`), f.tenant.Team(), f.ref.RunID, f.ref.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var ordinal int64
		var state string
		var finished *int64
		if err := rows.Scan(&ordinal, &state, &finished); err != nil {
			t.Fatal(err)
		}
		want := map[int64]string{first.Attempt: "failed", second.Attempt: "passed"}[ordinal]
		if state != want || finished == nil {
			t.Fatalf("attempt %d step=%q %v; want %s", ordinal, state, finished, want)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("step attempts=%d", count)
	}
}

func TestMetadataAnnotationByteCapacityRollsBackTheRejectedAppend(t *testing.T) {
	for _, step := range []string{"", "body"} {
		t.Run(step, func(t *testing.T) {
			f := newStoreRecoveryFixture(t, true)
			metadataStep(t, f, "body", "running")
			write := store.AnnotationWrite{Ref: f.ref, Actor: f.holder, StepID: step, Message: strings.Repeat("x", int(limits.AnnotationBytes)-len(`{"level":"","message":""}`))}
			accepted := 0
			for {
				err := f.tenant.WriteAnnotation(t.Context(), write)
				if err == nil {
					accepted++
					continue
				}
				var refused *apierr.Error
				if !errors.As(err, &refused) || refused.Code != apierr.LimitExceeded {
					t.Fatalf("byte refusal: %v", err)
				}
				break
			}
			entries, _ := metadataText(t, f, step)
			if accepted != int(limits.RunAnnotationBytes/limits.AnnotationBytes)-1 || len(entries) != accepted {
				t.Fatalf("stored %d entries, accepted %d", len(entries), accepted)
			}
			if err := f.tenant.WriteAnnotation(t.Context(), write); err == nil {
				t.Fatal("repeated over-budget append accepted")
			}
			after, _ := metadataText(t, f, step)
			if !reflect.DeepEqual(entries, after) {
				t.Fatal("byte refusal rewrote prior entries")
			}
		})
	}
}

func TestMetadataStepClosureRollsBackWithTheJobReport(t *testing.T) {
	f := newStoreRecoveryFixture(t, true)
	metadataStep(t, f, "body", "running")
	attempt := metadataAttempt(t, f)
	if _, err := f.s.DB().ExecContext(t.Context(), `CREATE TRIGGER refuse_metadata_completion BEFORE INSERT ON events BEGIN SELECT RAISE(ABORT,'refuse completion'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := f.tenant.TransitionNode(t.Context(), store.ReportNode{Ref: f.ref, Actor: f.holder, SourceAttempt: (*runspec.AttemptOrdinal)(&attempt.Attempt), Outcome: runstate.NodeSuccess, Result: &runstate.NodeResult{Outcome: runstate.NodeSuccess}})
	if err == nil {
		t.Fatal("report committed without completion event")
	}
	row, err := f.tenant.ReadNode(t.Context(), f.ref)
	if err != nil || row.Status != runstate.StatusRunning || row.Attempt != runspec.AttemptOrdinal(attempt.Attempt) {
		t.Fatalf("refused report changed job: %+v, %v", row, err)
	}
	var state string
	var finished *int64
	if err := f.s.DB().QueryRowContext(t.Context(), `SELECT status,finished_at FROM node_steps WHERE team=? AND run_id=? AND node_id=? AND attempt=? AND step_id='body'`, f.tenant.Team(), f.ref.RunID, f.ref.NodeID, attempt.Attempt).Scan(&state, &finished); err != nil || state != "running" || finished != nil {
		t.Fatalf("refused report changed step: %q %v, %v", state, finished, err)
	}
}
