package main

import (
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/opsview"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

type admissionProgress struct {
	queue controllerwire.QueueManager
	waits map[string]*admissionWait
}

type admissionWait struct {
	record  controllerwire.LogRecord
	next    time.Time
	step    int
	last    string
	printed bool
}

func admissionProgressFor(inv clisurface.Invocation, reader controllerwire.LogReader, query controllerwire.LogQuery) *admissionProgress {
	queue, ok := reader.(controllerwire.QueueManager)
	if !ok || !inv.Bool("follow") || runLogOutput(inv) == "json" || query.NoEvents || query.Grep != "" || query.Lines != "" || query.Head != nil {
		return nil
	}
	return &admissionProgress{queue: queue, waits: map[string]*admissionWait{}}
}

func (p *admissionProgress) observe(record controllerwire.LogRecord) bool {
	if p == nil {
		return false
	}
	id := string(wingwire.NodeHostParticipant(record.RunID, record.JobID))
	switch record.Event {
	case runstate.EventAdmissionQueued:
		if p.waits[id] == nil {
			p.waits[id] = &admissionWait{record: record}
		}
		return true
	case runstate.EventAdmissionGranted:
		delete(p.waits, id)
	}
	return false
}

func (p *admissionProgress) write(ctx context.Context, out io.Writer, format string, now time.Time) error {
	if p == nil {
		return nil
	}
	due := slices.Sorted(maps.Keys(p.waits))
	due = slices.DeleteFunc(due, func(id string) bool { return now.Before(p.waits[id].next) })
	if len(due) == 0 {
		return nil
	}
	state, readErr := p.queue.ReadQueue(ctx)
	// safety: logical reservations in the public queue duplicate machine holders without a machine charge.
	state.Holders = slices.DeleteFunc(slices.Clone(state.Holders), func(h wingwire.Holder) bool {
		return h.Resources.Cores == 0 && h.Resources.MemoryBytes == 0
	})
	state.Waiters = slices.DeleteFunc(slices.Clone(state.Waiters), func(w wingwire.Waiter) bool {
		return w.Resources.Cores == 0 && w.Resources.MemoryBytes == 0
	})
	for _, id := range due {
		wait := p.waits[id]
		intervals := [...]time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute}
		wait.next = now.Add(intervals[min(wait.step, len(intervals)-1)])
		wait.step++
		found := false
		if readErr == nil {
			for _, waiter := range state.Waiters {
				participant := waiter.ParticipantID
				if participant == "" {
					participant = waiter.RunID
				}
				if participant != id {
					continue
				}
				found = true
				key := admissionMaterialState(state, waiter)
				if !wait.printed || wait.last != key {
					if _, err := fmt.Fprintf(out, "%s: %s\n", wait.record.JobID, opsview.AdmissionWaitLine(state, waiter)); err != nil {
						return err
					}
					wait.printed, wait.last = true, key
				}
				break
			}
		}
		if !found && !wait.printed {
			if err := writeLogRecord(out, wait.record, format, false); err != nil {
				return err
			}
			wait.printed = true
		}
	}
	return nil
}

func admissionMaterialState(state wingwire.QueueState, waiter wingwire.Waiter) string {
	var holders []string
	for _, holder := range state.Holders {
		if !holder.ConnectionOnly && !holder.AdmissionWaiting {
			holders = append(holders, fmt.Sprintf("%s/%s:%v", holder.RunID, holder.ParticipantID, holder.Resources))
		}
	}
	slices.Sort(holders)
	waitingOn := slices.Sorted(slices.Values(waiter.WaitingOn))
	return fmt.Sprintf("%d:%v:%v", waiter.Position, waitingOn, holders)
}
