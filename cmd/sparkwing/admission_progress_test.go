package main

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

type admissionLogReader struct {
	fakeLogReader
	state wingwire.QueueState
	err   error
	reads int
}

func (r *admissionLogReader) ReadQueue(context.Context) (wingwire.QueueState, error) {
	r.reads++
	return r.state, r.err
}

func (*admissionLogReader) ReadQueueGroup(context.Context, string) (controllerwire.QueueGroup, error) {
	panic("unexpected group read")
}

func admissionLogFixture() (*admissionLogReader, controllerwire.LogRecord) {
	record := controllerwire.LogRecord{RunID: "canonical-run", JobID: "build/assets", Event: runstate.EventAdmissionQueued, Line: `{"position":1,"blocking_reason":"needs 8 cores"}`}
	return &admissionLogReader{state: wingwire.QueueState{
		Resources: []wingwire.ResourceState{{Key: "cores", Capacity: 8, Held: 4, Available: 4}},
		Holders:   []wingwire.Holder{{RunID: "holder", Pipeline: "check", Resources: wingwire.HostResources{Cores: 4}}},
		Waiters: []wingwire.Waiter{{
			RunID: record.RunID, ParticipantID: string(wingwire.NodeHostParticipant(record.RunID, record.JobID)),
			Position: 1, Resources: wingwire.HostResources{Cores: 8}, WaitingOn: []string{"cores"},
		}},
	}}, record
}

func TestAdmissionProgressMaterialChangesAndIntervals(t *testing.T) {
	reader, record := admissionLogFixture()
	p := admissionProgressFor(logInvocation(t, "logs", "--run", "412", "--follow", "-o", "plain"), reader, controllerwire.LogQuery{})
	if !p.observe(record) {
		t.Fatal("queued event was not handled")
	}
	var out bytes.Buffer
	start := time.Unix(1000, 0)
	for _, step := range []struct {
		seconds int
		change  func()
		reads   int
		lines   int
	}{
		{0, func() {}, 1, 1},
		{29, func() { reader.state.Holders = nil }, 1, 1},
		{30, func() {
			reader.state.Holders = []wingwire.Holder{{RunID: "holder", Resources: wingwire.HostResources{Cores: 4}}}
			reader.state.Resources[0].Available = 4.1
			clear := int64(120000)
			reader.state.ExpectedClearMS = &clear
			reader.state.Waiters = append(reader.state.Waiters, wingwire.Waiter{Resources: wingwire.HostResources{Cores: 1}})
		}, 2, 1},
		{89, func() { reader.state.Holders = nil }, 2, 1},
		{90, func() {}, 3, 2},
		{209, func() { reader.state.Waiters[0].Position = 2 }, 3, 2},
		{210, func() {}, 4, 3},
		{509, func() { reader.state.Waiters[0].WaitingOn = []string{"memory"} }, 4, 3},
		{510, func() {}, 5, 4},
		{810, func() {
			reader.state.Waiters[0].WaitingMS = 810000
			reader.state.Resources[0].External = 0.2
			*reader.state.ExpectedClearMS = 60000
		}, 6, 4},
		{1110, func() { reader.state.Waiters[0].WaitingOn = []string{"cores"} }, 7, 5},
	} {
		step.change()
		p.observe(record)
		if err := p.write(t.Context(), &out, "plain", start.Add(time.Duration(step.seconds)*time.Second)); err != nil {
			t.Fatal(err)
		}
		if reader.reads != step.reads || strings.Count(out.String(), "\n") != step.lines {
			t.Fatalf("at %ds: reads=%d lines=%d output=%s", step.seconds, reader.reads, strings.Count(out.String(), "\n"), &out)
		}
	}
	record.Event = runstate.EventAdmissionGranted
	p.observe(record)
	if err := p.write(t.Context(), &out, "plain", start.Add(time.Hour)); err != nil || reader.reads != 7 {
		t.Fatalf("granted waiter kept reading progress: reads=%d err=%v", reader.reads, err)
	}
	record.Event = runstate.EventAdmissionQueued
	p.observe(record)
	if err := p.write(t.Context(), &out, "plain", start.Add(time.Hour)); err != nil || reader.reads != 8 || strings.Count(out.String(), "\n") != 6 {
		t.Fatalf("new admission wait did not report entry: reads=%d output=%s err=%v", reader.reads, &out, err)
	}
}

func TestAdmissionProgressPreservesFilteredAndHistoricalLogs(t *testing.T) {
	for _, args := range [][]string{
		{"--grep", "needs"}, {"--lines", "1:3"}, {"--head", "5"}, {"--no-events"},
	} {
		inv := logInvocation(t, append([]string{"logs", "--run", "412", "--follow", "-o", "plain"}, args...)...)
		query, err := parseRunLogs(inv)
		if err != nil {
			t.Fatal(err)
		}
		reader, _ := admissionLogFixture()
		if admissionProgressFor(inv, reader, query) != nil {
			t.Fatalf("filtered log query changed: %v", args)
		}
	}
	reader, record := admissionLogFixture()
	reader.read = func(context.Context, string, controllerwire.LogQuery) (controllerwire.LogPage, error) {
		return controllerwire.LogPage{Records: []controllerwire.LogRecord{record}, Complete: true}, nil
	}
	var out bytes.Buffer
	if err := observeRunLogs(t.Context(), logInvocation(t, "logs", "--run", "412", "--follow", "-o", "plain"), reader, nil, &out, &out, nil); err != nil {
		t.Fatal(err)
	}
	if reader.reads != 0 || !strings.Contains(out.String(), "blocking_reason=needs 8 cores") {
		t.Fatalf("historical events changed: %s reads=%d", &out, reader.reads)
	}
}

func TestAdmissionProgressUsesOneHumanStreamAndPreservesJSON(t *testing.T) {
	for _, format := range []string{"plain", "pretty", "json"} {
		t.Run(format, func(t *testing.T) {
			reader, record := admissionLogFixture()
			reader.state.Holders = append(reader.state.Holders, wingwire.Holder{RunID: "logical-reservation", Semaphores: []string{"plan"}})
			reader.state.Waiters = append(reader.state.Waiters, wingwire.Waiter{RunID: "logical-waiter", WaitingOn: []string{"plan"}})
			calls := 0
			reader.read = func(context.Context, string, controllerwire.LogQuery) (controllerwire.LogPage, error) {
				calls++
				if calls <= 2 {
					return controllerwire.LogPage{Records: []controllerwire.LogRecord{record}, NextCursor: strconv.Itoa(calls)}, nil
				}
				return controllerwire.LogPage{Records: []controllerwire.LogRecord{record, {RunID: record.RunID, JobID: record.JobID, Event: runstate.EventAdmissionGranted}}, NextCursor: "3", Complete: true}, nil
			}
			var out bytes.Buffer
			inv := logInvocation(t, "logs", "--run", "412", "--follow", "-o", format)
			if err := observeRunLogs(t.Context(), inv, reader, nil, &out, &out, func(context.Context, time.Duration) error { return nil }); err != nil {
				t.Fatal(err)
			}
			if format == "json" {
				if reader.reads != 0 || strings.Count(out.String(), `"event":"job_admission_queued"`) != 3 || !strings.Contains(out.String(), `\"position\":1`) {
					t.Fatalf("JSON changed: %s reads=%d", &out, reader.reads)
				}
			} else if reader.reads != 1 || strings.Count(out.String(), "admission:") != 1 || !strings.Contains(out.String(), "1 running (check 4 cores), 1 queued") || strings.Contains(out.String(), "blocking_reason=") || !strings.Contains(out.String(), "admission granted") && !strings.Contains(out.String(), runstate.EventAdmissionGranted) {
				t.Fatalf("human stream: %s reads=%d", &out, reader.reads)
			}
		})
	}
}

func TestAdmissionProgressQueueUnavailableKeepsWaitReason(t *testing.T) {
	reader, record := admissionLogFixture()
	reader.err = errors.New("queue unavailable")
	p := admissionProgressFor(logInvocation(t, "logs", "--run", "412", "--follow", "-o", "plain"), reader, controllerwire.LogQuery{})
	p.observe(record)
	var out bytes.Buffer
	now := time.Unix(1000, 0)
	for _, elapsed := range []time.Duration{0, 30 * time.Second} {
		if err := p.write(t.Context(), &out, "plain", now.Add(elapsed)); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(out.String(), "needs 8 cores") || strings.Count(out.String(), "\n") != 1 {
		t.Fatal(out.String())
	}
}

func TestAdmissionProgressFanOutBoundsQueueReorderOutput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reader, record := admissionLogFixture()
		reader.state.Waiters = nil
		var records []controllerwire.LogRecord
		for i := range 3 {
			record.JobID = "job-" + strconv.Itoa(i)
			records = append(records, record)
			reader.state.Waiters = append(reader.state.Waiters, wingwire.Waiter{
				RunID: record.RunID, ParticipantID: string(wingwire.NodeHostParticipant(record.RunID, record.JobID)),
				Position: i + 1, Resources: wingwire.HostResources{Cores: 8}, WaitingOn: []string{"cores"},
			})
		}
		calls := 0
		reader.read = func(context.Context, string, controllerwire.LogQuery) (controllerwire.LogPage, error) {
			calls++
			for i := range reader.state.Waiters {
				reader.state.Waiters[i].Position = (i+calls)%3 + 1
			}
			if calls == 51 {
				for i := range records {
					records[i].Event = runstate.EventAdmissionGranted
					records[i].Line = ""
				}
			}
			return controllerwire.LogPage{Records: records, NextCursor: strconv.Itoa(calls), Complete: calls == 51}, nil
		}
		var out bytes.Buffer
		inv := logInvocation(t, "logs", "--run", "412", "--follow", "-o", "plain")
		if err := observeRunLogs(t.Context(), inv, reader, nil, &out, &out, func(context.Context, time.Duration) error { return nil }); err != nil {
			t.Fatal(err)
		}
		if reader.reads != 1 || strings.Count(out.String(), "admission:") != 3 || strings.Count(out.String(), "admission granted") != 3 || strings.Contains(out.String(), "blocking_reason=") {
			t.Fatalf("fan-out produced reorder chatter: reads=%d output=%s", reader.reads, &out)
		}
	})
}
