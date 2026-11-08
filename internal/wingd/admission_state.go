package wingd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// MachineAdmissionStore atomically stores the ledger and request replay receipts.
type MachineAdmissionStore interface {
	LoadMachineAdmission(context.Context) ([]byte, error)
	SaveMachineAdmission(context.Context, []byte) error
}

const (
	maxTerminalAdmissionTickets = 256
	terminalAdmissionRetention  = time.Minute
)

type admissionTicket struct {
	TerminalAt       time.Time              `json:"terminal_at,omitempty"`
	TerminalRevision uint64                 `json:"terminal_revision,omitempty"`
	MeasurementPID   int                    `json:"measurement_pid,omitempty"`
	MeasurementBirth string                 `json:"measurement_birth,omitempty"`
	Fingerprint      string                 `json:"fingerprint"`
	Subject          string                 `json:"subject"`
	Input            AdmissionHTTPRequest   `json:"input"`
	Result           AdmissionHTTPState     `json:"result"`
	Started          time.Time              `json:"started"`
	Charge           wingwire.HostResources `json:"charge"`
	LeaseID          admission.LeaseID      `json:"lease_id,omitempty"`
	Active           bool                   `json:"active"`
	participant      *conn
}

type durableAdmission struct {
	Revision      uint64             `json:"revision"`
	Snapshot      admission.Snapshot `json:"snapshot"`
	Tickets       []*admissionTicket `json:"tickets"`
	Events        []admissionEvent   `json:"events,omitempty"`
	CancelledRuns []string           `json:"cancelled_runs,omitempty"`
}

func admissionRequestKey(subject, key string) string { return subject + "\x00" + key }

func (d *Daemon) admissionPayloadLocked() ([]byte, error) {
	tickets := make([]*admissionTicket, 0, len(d.admissionTickets))
	for _, ticket := range d.admissionTickets {
		tickets = append(tickets, ticket)
	}
	sort.Slice(tickets, func(i, j int) bool { return tickets[i].Result.ID < tickets[j].Result.ID })
	return json.Marshal(durableAdmission{Revision: d.admissionRevision, Snapshot: d.ledger.Snapshot(), Tickets: tickets, Events: d.events.snapshot(d.now()), CancelledRuns: d.cancelledRunOrder})
}

func (d *Daemon) restoreAdmissionPayloadLocked(data []byte) error {
	var state durableAdmission
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("wingd: decode machine admission: %w", err)
	}
	ledger, err := admission.Restore(state.Snapshot, nil)
	if err != nil {
		return fmt.Errorf("wingd: restore machine admission: %w", err)
	}
	d.ledger = ledger
	d.admissionRevision = state.Revision
	d.appliedCores = float64(state.Snapshot.HeadroomMilliCores) / 1000
	d.appliedMem = state.Snapshot.HeadroomMemoryBytes
	d.admissionTickets = map[string]*admissionTicket{}
	d.admissionKeys = map[string]string{}
	d.conns = map[*conn]struct{}{}
	d.byRun = map[string]*conn{}
	d.leaseRun = map[admission.LeaseID]string{}
	d.leaseCharge = map[admission.LeaseID]wingwire.HostResources{}
	d.leaseMembers = map[admission.LeaseID][]string{}
	d.events.restore(d.now(), state.Events)
	d.cancelledRuns = map[string]struct{}{}
	d.cancelledRunOrder = nil
	for _, id := range state.CancelledRuns {
		d.recordCancelledRunLocked(id)
	}
	for _, ticket := range state.Tickets {
		if ticket.Result.ID == "" || ticket.Input.RequestKey == "" {
			return fmt.Errorf("wingd: machine admission ticket identity missing")
		}
		d.admissionTickets[ticket.Result.ID] = ticket
		d.admissionKeys[admissionRequestKey(ticket.Subject, ticket.Input.RequestKey)] = ticket.Result.ID
		if !ticket.Active {
			continue
		}
		c := d.ticketParticipant(ticket)
		ticket.participant = c
		d.byRun[c.runID] = c
		d.conns[c] = struct{}{}
		if ticket.LeaseID != "" {
			c.role = roleHolder
			c.leaseID = ticket.LeaseID
			c.members = []string{c.runID}
			d.leaseRun[c.leaseID] = c.runID
			d.leaseCharge[c.leaseID] = c.resources
			d.leaseMembers[c.leaseID] = c.members
		} else {
			c.role = roleWaiter
		}
	}
	return nil
}

func (d *Daemon) ticketParticipant(ticket *admissionTicket) *conn {
	req := ticket.Input.Request
	return &conn{
		measurementPID: ticket.MeasurementPID, measurementBirth: ticket.MeasurementBirth, ticketID: ticket.Result.ID, runID: string(req.RunID), journalRunID: string(req.RunID), ownerRunID: req.OwnerRunID,
		displayRunID: req.DisplayRunID, pipeline: req.Pipeline, priority: req.Priority, class: string(admission.NormalizeWorkloadClass(admission.WorkloadClass(req.Class))),
		pid: ticket.Input.RootPID, resources: ticket.Charge, sems: semNames(req.Semaphores), startAt: ticket.Started,
		costSource: string(req.CostSource), repo: req.Repo, expectedDurationMS: req.ExpectedDurationMS, expectedP99MS: req.ExpectedP99MS,
		sampleCount: req.SampleCount, driftWarning: req.DriftWarning, origin: req.Origin,
		finalizable: !req.SubLease,
	}
}

func (d *Daemon) captureAdmissionDeliveriesLocked(deliveries []delivery) {
	for _, dl := range deliveries {
		ticket := d.admissionTickets[dl.c.ticketID]
		if ticket == nil {
			continue
		}
		switch msg := dl.msg.(type) {
		case *wingwire.Grant:
			ticket.Result.State = "granted"
			ticket.Result.Grant = msg
			ticket.Result.Queued = nil
			ticket.LeaseID = dl.c.leaseID
			ticket.Started = dl.c.startAt
		case *wingwire.Queued:
			if ticket.Result.State == "cancelled" || ticket.Result.State == "evicted" {
				continue
			}
			if prior := ticket.Result.Queued; prior != nil && *prior != *msg {
				d.recordJournal("queued", dl.c, map[string]any{"position": msg.Position, "key": msg.Key, "blocking_reason": msg.BlockingReason})
			}
			ticket.Result.State = "queued"
			ticket.Result.Queued = msg
		case *wingwire.Evicted:
			ticket.Result.State = "evicted"
			ticket.Result.Evicted = msg
			ticket.Result.Grant = nil
			ticket.Result.Queued = nil
		case *wingwire.Cancel:
			ticket.Result.State = "cancelled"
			ticket.Result.Cancel = msg
			ticket.Result.Grant = nil
			ticket.Result.Queued = nil
		}
	}
	snap := d.ledger.Snapshot()
	for _, w := range snap.Waiters {
		if c := d.byRun[w.RequestID]; c != nil {
			if t := d.admissionTickets[c.ticketID]; t != nil {
				t.Result.Arrival = w.Arrival
			}
		}
	}
}

func (d *Daemon) pruneAdmissionTicketsLocked() {
	now := d.now()
	var terminal []*admissionTicket
	remove := func(ticket *admissionTicket) {
		delete(d.admissionTickets, ticket.Result.ID)
		delete(d.admissionKeys, admissionRequestKey(ticket.Subject, ticket.Input.RequestKey))
	}
	for _, ticket := range d.admissionTickets {
		if ticket.Active || ticket.MeasurementPID != 0 {
			continue
		}
		if ticket.TerminalAt.IsZero() {
			ticket.TerminalAt = now
			ticket.TerminalRevision = d.admissionRevision + 1
		}
		if now.Sub(ticket.TerminalAt) >= terminalAdmissionRetention {
			remove(ticket)
			continue
		}
		terminal = append(terminal, ticket)
	}
	if len(terminal) <= maxTerminalAdmissionTickets {
		return
	}
	sort.Slice(terminal, func(i, j int) bool {
		if terminal[i].TerminalRevision != terminal[j].TerminalRevision {
			return terminal[i].TerminalRevision < terminal[j].TerminalRevision
		}
		return terminal[i].Result.ID < terminal[j].Result.ID
	})
	for _, ticket := range terminal[:len(terminal)-maxTerminalAdmissionTickets] {
		remove(ticket)
	}
}

func (d *Daemon) commitAdmissionLocked() error {
	d.captureAdmissionDeliveriesLocked(d.waiterDeliveriesLocked())
	d.pruneAdmissionTicketsLocked()
	d.admissionRevision++
	for _, ticket := range d.admissionTickets {
		ticket.Result.Revision = d.admissionRevision
	}
	data, err := d.admissionPayloadLocked()
	if err == nil && d.admissionStore != nil {
		err = d.admissionStore.SaveMachineAdmission(d.admissionContext, data)
	}
	if err != nil {
		if len(d.admissionCommitted) > 0 {
			if restoreErr := d.restoreAdmissionPayloadLocked(d.admissionCommitted); restoreErr != nil {
				return errors.Join(err, fmt.Errorf("restore committed admission: %w", restoreErr))
			}
		}
		return fmt.Errorf("wingd: commit machine admission: %w", err)
	}
	d.admissionCommitted = data
	if d.admissionChanged != nil {
		close(d.admissionChanged)
	}
	d.admissionChanged = make(chan struct{})
	return nil
}
