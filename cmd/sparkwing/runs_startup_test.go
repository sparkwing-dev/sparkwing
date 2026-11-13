package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

type startupReader struct {
	*statusReader
	fakeLogReader
}

func TestStartupAppearsInRunStatusAndTiming(t *testing.T) {
	run, steps, events := timingFixture()
	subject := blob.LogSubject{Kind: blob.LogOwnerUnit, Team: "team", RunID: run.ID, Unit: &blob.LogUnit{Kind: blob.LogPlan, Generation: 2}}
	run.Logs = []blob.LogSubject{subject}
	claim := controllerwire.LogRecord{RunID: run.ID, Event: "owner_claimed", Time: at(-20), Line: `{"kind":"plan","generation":2,"claimed_at":"2026-11-04T11:59:40Z"}`}
	cloud := controllerwire.LogRecord{RunID: run.ID, Event: "cloud_startup", Time: at(-15), Line: `{"award":{"kind":"plan","generation":2},"unit":{"Kind":"plan"},"startup":{"pod_uid":"pod","scheduled_at":"2026-11-04T11:59:41Z","image_pulled_at":"2026-11-04T11:59:43Z","container_started_at":"2026-11-04T11:59:44Z"}}`}
	events = append(events, claim, cloud)
	preparation := controllerwire.LogRecord{RunID: run.ID, Subject: &subject, Time: at(-5), LineNo: 1, Line: `{"event":"preparation","attrs":{"stage":"sdk_install","started_at":"2026-11-04T11:59:45Z","duration_ms":10000,"success":true,"cache":{"outcome":"compile","bytes_fetched":0}}}`}
	logs := fakeLogReader{read: func(_ context.Context, _ string, q controllerwire.LogQuery) (controllerwire.LogPage, error) {
		if q.EventsOnly {
			return controllerwire.LogPage{Records: events, Complete: true}, nil
		}
		return controllerwire.LogPage{Records: []controllerwire.LogRecord{preparation}, Complete: true}, nil
	}}
	for _, format := range []string{"json", "pretty"} {
		t.Run("status/"+format, func(t *testing.T) {
			reader := startupReader{statusReader: &statusReader{details: []controllerwire.RunDetail{run}, steps: controllerwire.RunSteps{Steps: steps}}, fakeLogReader: logs}
			var out bytes.Buffer
			_ = observeRunStatus(t.Context(), statusInvocation(t, "-o", format), reader, &out, nil)
			for _, want := range []string{"pod_scheduled", "image_pulled", "container_started", "claim", "sdk_ready", "compile"} {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("missing %s: %s", want, &out)
				}
			}
			if format == "json" && !strings.Contains(out.String(), `"bytes_fetched":0`) {
				t.Fatalf("missing known zero transfer: %s", &out)
			}
		})
	}
	reader := listingReader{list: func(context.Context, controllerwire.RunQuery) (controllerwire.RunPage, error) {
		return controllerwire.RunPage{Runs: []controllerwire.RunDetail{run}}, nil
	}, steps: func(context.Context, string) (controllerwire.RunSteps, error) {
		return controllerwire.RunSteps{Steps: steps}, nil
	}}
	inv, err := clisurface.Parse("sparkwing", []string{"runs", "timing", "--pipeline", "check", "--last", "1", "-o", "json"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := observeRunsTiming(t.Context(), inv, reader, logs, &out); err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(bytes.Split(out.Bytes(), []byte("\n"))[0], &got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got["startup"], []byte(`"sdk_ready"`)) || !bytes.Contains(got["startup"], []byte(`"generation":2`)) {
		t.Fatalf("timing lost startup identity: %s", &out)
	}
}

func TestStartupReadFailureLeavesStatusAvailable(t *testing.T) {
	run := controllerwire.RunDetail{ID: "run", Status: "success"}
	reader := startupReader{statusReader: &statusReader{details: []controllerwire.RunDetail{run}}, fakeLogReader: fakeLogReader{read: func(context.Context, string, controllerwire.LogQuery) (controllerwire.LogPage, error) {
		return controllerwire.LogPage{}, errors.New("private transport diagnostic")
	}}}
	var out bytes.Buffer
	if err := observeRunStatus(t.Context(), statusInvocation(t), reader, &out, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "startup_unavailable") || strings.Contains(out.String(), "private transport diagnostic") {
		t.Fatalf("unavailable diagnostic replaced or contaminated status: %s", &out)
	}
}

func TestStatusFollowRefreshesStartupWithoutAStateTransition(t *testing.T) {
	run := controllerwire.RunDetail{ID: "run", Status: "pending", Logs: []blob.LogSubject{{Kind: blob.LogOwnerUnit, Team: "team", RunID: "run", Unit: &blob.LogUnit{Kind: blob.LogPlan, Generation: 1}}}}
	reads := 0
	reader := startupReader{statusReader: &statusReader{details: []controllerwire.RunDetail{run}}, fakeLogReader: fakeLogReader{read: func(_ context.Context, _ string, q controllerwire.LogQuery) (controllerwire.LogPage, error) {
		if q.EventsOnly {
			return controllerwire.LogPage{Complete: true}, nil
		}
		reads++
		stage := "checkout"
		if reads > 1 {
			stage = "sdk_install"
		}
		return controllerwire.LogPage{Complete: true, Records: []controllerwire.LogRecord{{RunID: run.ID, Subject: &run.Logs[0], Line: `{"event":"preparation","attrs":{"stage":"` + stage + `","started_at":"2026-11-04T11:59:45Z","duration_ms":10,"success":true}}`}}}, nil
	}}}
	var out bytes.Buffer
	waits := 0
	_ = observeRunStatus(t.Context(), statusInvocation(t, "--follow"), reader, &out, func(context.Context, time.Duration) error {
		waits++
		if waits == 2 {
			return context.Canceled
		}
		return nil
	})
	if reads != 2 || bytes.Count(out.Bytes(), []byte("\n")) != 2 || !strings.Contains(out.String(), "sdk_ready") {
		t.Fatalf("unchanged run hid startup progress: reads=%d %s", reads, &out)
	}
}
