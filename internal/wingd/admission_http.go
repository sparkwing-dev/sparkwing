package wingd

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

const (
	admissionHTTPPath     = "/api/v1/local/admission"
	admissionPollDuration = 20 * time.Second
)

func (d *Daemon) admissionHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, admissionHTTPPath+"/") {
			next.ServeHTTP(w, r)
			return
		}
		peer, ok := localhostwire.LocalPeer(r.Context())
		owner, err := controllerPeerIdentity()
		if !ok || err != nil || !verifiedSocketPeer(r.Context()) {
			writeLoginFailure(w, &apierr.Error{Code: apierr.Unauthenticated, Message: "machine admission requires a verified local socket peer"})
			return
		}
		subject, err := LocalSubject(peer, owner)
		if err != nil {
			writeLoginFailure(w, &apierr.Error{Code: apierr.Forbidden, Message: err.Error()})
			return
		}
		reply := &admissionResponseBuffer{header: make(http.Header)}
		defer reply.send(w)
		w = reply
		path := strings.TrimPrefix(r.URL.Path, admissionHTTPPath)
		switch {
		case path == "/requests" && r.Method == http.MethodPost:
			d.requestAdmissionHTTP(w, r, subject, peer)
		case path == "/queue" && r.Method == http.MethodGet:
			if err := d.reapAdmission(); err != nil {
				admissionHTTPFailure(w, err)
				return
			}
			writeAdmissionJSON(w, d.readQueueState())
		case path == "/priority" && r.Method == http.MethodPost:
			d.priorityAdmissionHTTP(w, r)
		case path == "/cancel" && r.Method == http.MethodPost:
			d.cancelAdmissionHTTP(w, r)
		case path == "/stats/reset" && r.Method == http.MethodPost:
			d.mu.Lock()
			d.events.reset()
			err := d.commitAdmissionLocked()
			d.mu.Unlock()
			if err != nil {
				admissionHTTPFailure(w, err)
				return
			}
			writeAdmissionJSON(w, &wingwire.StatsResetAck{})
		case strings.HasPrefix(path, "/requests/by-key/") && r.Method == http.MethodDelete:
			key := strings.TrimPrefix(path, "/requests/by-key/")
			if key == "" {
				writeLoginFailure(w, &apierr.Error{Code: apierr.BadRequest, Message: "request key is required"})
				return
			}
			d.mu.Lock()
			id := d.admissionKeys[admissionRequestKey(subject, key)]
			d.mu.Unlock()
			if id == "" {
				d.mu.Lock()
				id = d.admissionKeys[admissionRequestKey(subject, key)]
				if id == "" {
					id = rand.Text()
					ticket := &admissionTicket{Subject: subject, Input: AdmissionHTTPRequest{RequestKey: key}, Result: AdmissionHTTPState{ID: id, State: "released"}}
					d.admissionKeys[admissionRequestKey(subject, key)] = id
					d.admissionTickets[id] = ticket
					if err := d.commitAdmissionLocked(); err != nil {
						d.mu.Unlock()
						admissionHTTPFailure(w, err)
						return
					}
				}
				d.mu.Unlock()
			}
			d.releaseAdmissionHTTP(w, r, subject, id)
		case strings.HasPrefix(path, "/requests/") && strings.HasSuffix(path, "/process") && r.Method == http.MethodPost:
			id := strings.TrimSuffix(strings.TrimPrefix(path, "/requests/"), "/process")
			d.processAdmissionHTTP(w, r, subject, id, peer)
		case strings.HasPrefix(path, "/requests/"):
			id := strings.TrimPrefix(path, "/requests/")
			watch := strings.HasSuffix(id, "/watch")
			id = strings.TrimSuffix(id, "/watch")
			if id == "" || strings.Contains(id, "/") {
				http.NotFound(w, r)
				return
			}
			if r.Method == http.MethodDelete && !watch {
				d.releaseAdmissionHTTP(w, r, subject, id)
				return
			}
			if r.Method == http.MethodGet {
				d.getAdmissionHTTP(w, r, subject, id, watch)
				return
			}
			writeLoginFailure(w, &apierr.Error{Code: apierr.MethodNotAllowed, Message: "ticket requests use GET or DELETE"})
		case strings.HasPrefix(path, "/leases/") && r.Method == http.MethodGet:
			token := strings.TrimPrefix(path, "/leases/")
			d.mu.Lock()
			id := ""
			for _, ticket := range d.admissionTickets {
				if ticket.Subject == subject && ticket.Active && ticket.Result.Grant != nil && ticket.Result.Grant.LeaseToken == token {
					id = ticket.Result.ID
					break
				}
			}
			d.mu.Unlock()
			if id == "" {
				writeLoginFailure(w, &apierr.Error{Code: apierr.NotFound, Message: "admission lease not found"})
				return
			}
			d.getAdmissionHTTP(w, r, subject, id, false)
		default:
			writeLoginFailure(w, &apierr.Error{Code: apierr.UnsupportedRoute, Message: "unknown machine admission operation"})
		}
	})
}

func decodeAdmissionHTTP(w http.ResponseWriter, r *http.Request, target any) bool {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		writeLoginFailure(w, &apierr.Error{Code: apierr.BadRequest, Message: "machine admission writes require application/json"})
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxFrame))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeLoginFailure(w, &apierr.Error{Code: apierr.BadRequest, Message: "invalid machine admission request: " + err.Error()})
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeLoginFailure(w, &apierr.Error{Code: apierr.BadRequest, Message: "machine admission requires one JSON value"})
		return false
	}
	return true
}

func writeAdmissionJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		panic(http.ErrAbortHandler) //nolint:forbidigo // net/http aborts the incomplete response.
	}
}

func admissionHTTPFailure(w http.ResponseWriter, err error) {
	writeLoginFailure(w, &apierr.Error{Code: apierr.Unavailable, Message: err.Error()})
}

func (d *Daemon) requestAdmissionHTTP(w http.ResponseWriter, r *http.Request, subject string, peer localhostwire.LocalPeerIdentity) {
	var input AdmissionHTTPRequest
	if !decodeAdmissionHTTP(w, r, &input) {
		return
	}
	if input.RequestKey == "" || input.RootPID <= 0 || input.ProcessBirth == "" {
		writeLoginFailure(w, &apierr.Error{Code: apierr.BadRequest, Message: "request_key, root_pid and process_birth are required"})
		return
	}
	fingerprint, err := json.Marshal(input)
	if err != nil {
		writeLoginFailure(w, &apierr.Error{Code: apierr.BadRequest, Message: err.Error()})
		return
	}
	key := admissionRequestKey(subject, input.RequestKey)
	replay := func() bool {
		id := d.admissionKeys[key]
		if id == "" {
			return false
		}
		ticket := d.admissionTickets[id]
		if ticket.Fingerprint != "" && ticket.Fingerprint != string(fingerprint) {
			writeLoginFailure(w, &apierr.Error{Code: apierr.Conflict, Message: "request key already identifies different admission inputs"})
		} else {
			writeAdmissionJSON(w, ticket.Result)
		}
		return true
	}
	d.mu.Lock()
	if replay() {
		d.mu.Unlock()
		return
	}
	d.mu.Unlock()
	var terminal bool
	var terminalErr error
	if d.cfg.Runs != nil {
		terminal, terminalErr = d.cfg.Runs.IsRunTerminal(input.Request.RunID.RunID())
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.reapAdmissionLocked(); err != nil {
		admissionHTTPFailure(w, err)
		return
	}
	if replay() {
		return
	}
	if err := verifyAdmissionProcess(peer, input.RootPID, input.ProcessBirth); err != nil {
		writeLoginFailure(w, &apierr.Error{Code: apierr.Forbidden, Message: err.Error()})
		return
	}
	req := input.Request
	if req.PID != 0 && req.PID != input.RootPID {
		writeLoginFailure(w, &apierr.Error{Code: apierr.BadRequest, Message: "request PID must match root_pid"})
		return
	}
	req.PID = input.RootPID
	if !validCostSource(req.CostSource) {
		d.recordWindow(d.now(), admissionEvent{Kind: eventRejection, Key: "cost_source"}, &conn{runID: string(req.RunID), pid: req.PID}, map[string]any{"reason": "unknown admission cost source"})
		if err := d.commitAdmissionLocked(); err != nil {
			admissionHTTPFailure(w, err)
			return
		}
		writeLoginFailure(w, &apierr.Error{Code: apierr.BadRequest, Message: fmt.Sprintf("unknown admission cost source %q", req.CostSource)})
		return
	}
	if d.draining || d.shuttingDown {
		writeLoginFailure(w, &apierr.Error{Code: apierr.Unavailable, Message: "local controller is draining"})
		return
	}
	id := rand.Text()
	ticket := &admissionTicket{Subject: subject, Input: input, Result: AdmissionHTTPState{ID: id}, Fingerprint: string(fingerprint), Started: d.now()}
	identity := &conn{runID: string(req.RunID), displayRunID: req.DisplayRunID, pipeline: req.Pipeline, repo: req.Repo, pid: req.PID}
	requestData := map[string]any{"resources": req.Resources, "class": req.Class, "priority": req.Priority}
	if len(req.Semaphores) > 0 {
		requestData["semaphores"] = req.Semaphores
	}
	d.recordJournal("request", identity, requestData)
	d.admissionTickets[id] = ticket
	d.admissionKeys[key] = id
	deny := func(key, reason string, policy wingwire.Policy) {
		d.recordJournal("denied", identity, map[string]any{"reason": key, "decision": policy, "nonblocking": req.NonBlocking})
		if key == "invalid" {
			d.recordWindow(d.now(), admissionEvent{Kind: eventRejection, Key: "request"}, identity, map[string]any{"reason": reason})
		}
		ticket.Result.State = "evicted"
		ticket.Result.Evicted = &wingwire.Evicted{RunID: string(req.RunID), Key: key, Reason: reason, Policy: policy}
	}
	cancelled := terminal || d.runCancelledLocked(string(req.RunID))
	if !cancelled && terminalErr != nil {
		deny("terminal-check", fmt.Sprintf("controller %s could not read the runs store: %v", d.cfg.Version, terminalErr), wingwire.PolicyFail)
	} else if cancelled {
		deny("cancelled", "run is terminal or cancelled", wingwire.PolicyFail)
	} else if d.byRun[string(req.RunID)] != nil {
		deny("duplicate", "another request already holds this admission identity", wingwire.PolicyFail)
	} else {
		req.OwnerRunID = d.validatedOwnerRunIDLocked(req.OwnerRunID, req.OwnerLeaseToken)
		req.Priority = d.ledger.EffectivePriority(string(req.RunID), req.OwnerRunID, req.Priority)
		ticket.Input.Request = req
		charge := chargedResources(req.Resources)
		if req.SemaphoresOnly {
			charge = wingwire.HostResources{}
		} else {
			charge = d.clampHostChargeLocked(charge, req.CostSource)
		}
		ticket.Charge = charge
		participant := d.ticketParticipant(ticket)
		ticket.participant = participant
		ticket.Active = true
		d.byRun[participant.runID] = participant
		d.conns[participant] = struct{}{}
		ledgerCharge := charge
		if d.cfg.admissionPolicy().Mode == admission.ModeOff {
			ledgerCharge = wingwire.HostResources{}
		}
		request := requestFromWireWithMetadata(string(req.RunID), req.OwnerRunID, ledgerCharge, req.Semaphores, req.CostSource, req.Priority, req.Class, req.ExpectedP99MS)
		request.BurstCores = d.cfg.admissionPolicy().interactiveBurstEligible(&req, ledgerCharge)
		decision, events, err := d.ledger.Submit(request)
		if err != nil {
			d.removeAdmissionParticipantLocked(ticket)
			deny(submitErrorKey(err), refusalReason(err), wingwire.PolicyFail)
		} else {
			switch decision.Kind {
			case admission.DecisionQueued:
				participant.role = roleWaiter
				if req.NonBlocking {
					events = append(events, d.ledger.CancelWaiter(string(req.RunID))...)
					d.removeAdmissionParticipantLocked(ticket)
					deny("capacity", "capacity is not immediately available", wingwire.PolicyFail)
				}
			case admission.DecisionFailed, admission.DecisionSkipped:
				d.removeAdmissionParticipantLocked(ticket)
				policy := wingwire.PolicyFail
				if decision.Kind == admission.DecisionSkipped {
					policy = wingwire.PolicySkip
				}
				deny(decision.Key, "", policy)
			}
			d.routeLocked(events)
		}
	}
	if err := d.commitAdmissionLocked(); err != nil {
		admissionHTTPFailure(w, err)
		return
	}
	writeAdmissionJSON(w, ticket.Result)
}

func (d *Daemon) getAdmissionHTTP(w http.ResponseWriter, r *http.Request, subject, id string, watch bool) {
	timer := time.NewTimer(admissionPollDuration)
	defer timer.Stop()
	wait := r.URL.Query().Get("wait") == "true"
	after := r.URL.Query().Get("after")
	var cursor uint64
	if after != "" {
		var err error
		cursor, err = strconv.ParseUint(after, 10, 64)
		if err != nil {
			writeLoginFailure(w, &apierr.Error{Code: apierr.BadRequest, Message: "after must be a durable admission revision"})
			return
		}
	}
	var previous []byte
	var previousSequence uint64
	for {
		d.mu.Lock()
		if err := d.reapAdmissionLocked(); err != nil {
			d.mu.Unlock()
			admissionHTTPFailure(w, err)
			return
		}
		ticket := d.admissionTickets[id]
		if ticket == nil || ticket.Subject != subject {
			d.mu.Unlock()
			writeLoginFailure(w, &apierr.Error{Code: apierr.NotFound, Message: "admission ticket not found"})
			return
		}
		pending := ticket.Result.State == "queued" || watch && ticket.Active && ticket.Result.State == "granted"
		result := ticket.Result
		if after != "" && result.Revision != cursor {
			wait = false
		}
		changed := d.admissionChanged
		sequence := d.admissionRevision
		data, err := json.Marshal(result)
		d.mu.Unlock()
		if err != nil {
			admissionHTTPFailure(w, err)
			return
		}
		if previous != nil && (!bytes.Equal(previous, data) || sequence != previousSequence && !watch) {
			wait = false
		}
		previous = data
		previousSequence = sequence
		if !wait || !pending {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write(data)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-timer.C:
			wait = false
		case <-changed:
		}
	}
}

func (d *Daemon) releaseAdmissionHTTP(w http.ResponseWriter, _ *http.Request, subject, id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.reapAdmissionLocked(); err != nil {
		admissionHTTPFailure(w, err)
		return
	}
	ticket := d.admissionTickets[id]
	if ticket == nil || ticket.Subject != subject {
		writeLoginFailure(w, &apierr.Error{Code: apierr.NotFound, Message: "admission ticket not found"})
		return
	}
	if ticket.Active {
		events, err := d.releaseAdmissionTicketLocked(ticket)
		if err != nil {
			admissionHTTPFailure(w, err)
			return
		}
		if ticket.Result.State == "granted" || ticket.Result.State == "queued" {
			ticket.Result.State = "released"
			ticket.Result.Grant = nil
			ticket.Result.Queued = nil
		}
		d.routeLocked(events)
		if err := d.commitAdmissionLocked(); err != nil {
			admissionHTTPFailure(w, err)
			return
		}
	}
	writeAdmissionJSON(w, ticket.Result)
}

func (d *Daemon) removeAdmissionParticipantLocked(ticket *admissionTicket) {
	ticket.Active = false
	if c := ticket.participant; c != nil {
		delete(d.byRun, c.runID)
		delete(d.conns, c)
		c.role = roleNone
	}
}

func (d *Daemon) releaseAdmissionTicketLocked(ticket *admissionTicket) ([]admission.Event, error) {
	c := ticket.participant
	var events []admission.Event
	if c != nil {
		switch c.role {
		case roleWaiter:
			if c.finalizable {
				d.recordWindow(d.now(), admissionEvent{Kind: eventCancellation}, c, map[string]any{"reason": "queued admission cancelled"})
			}
			events = d.ledger.CancelWaiter(c.runID)
		case roleHolder:
			var err error
			events, err = d.ledger.Release(c.leaseID, c.runID)
			if err != nil {
				return nil, err
			}
		}
	}
	d.removeAdmissionParticipantLocked(ticket)
	return events, nil
}

func (d *Daemon) priorityAdmissionHTTP(w http.ResponseWriter, r *http.Request) {
	var request wingwire.SetPriority
	if !decodeAdmissionHTTP(w, r, &request) {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.reapAdmissionLocked(); err != nil {
		admissionHTTPFailure(w, err)
		return
	}
	targets := d.runConnsLocked(request.RunID)
	if len(targets) == 0 {
		writeAdmissionJSON(w, &wingwire.SetPriorityAck{Reason: "not in local admission"})
		return
	}
	priority := request.Priority
	if request.Mode != "" {
		var ok bool
		priority, ok = resolveRelativePriority(d.ledger.Snapshot(), request.RunID, request.Mode)
		if !ok {
			writeLoginFailure(w, &apierr.Error{Code: apierr.BadRequest, Message: "unknown priority mode"})
			return
		}
	}
	ack := wingwire.SetPriorityAck{Found: true, Previous: targets[0].priority, Priority: priority, Participants: len(targets)}
	for _, target := range targets {
		ack.Holding = ack.Holding || target.role == roleHolder
		target.priority = priority
		for _, ticket := range d.admissionTickets {
			if ticket.participant == target {
				ticket.Input.Request.Priority = priority
			}
		}
	}
	_, events := d.ledger.SetPriority(request.RunID, priority)
	d.routeLocked(events)
	ack.Position = runQueuePosition(d.ledger.Snapshot(), request.RunID)
	if err := d.commitAdmissionLocked(); err != nil {
		admissionHTTPFailure(w, err)
		return
	}
	writeAdmissionJSON(w, ack)
}

func (d *Daemon) cancelAdmissionHTTP(w http.ResponseWriter, r *http.Request) {
	var request wingwire.CancelLease
	if !decodeAdmissionHTTP(w, r, &request) {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.reapAdmissionLocked(); err != nil {
		admissionHTTPFailure(w, err)
		return
	}
	targets := d.runConnsLocked(request.RunID)
	found := len(targets) > 0
	if _, ok := d.cancelledRuns[request.RunID]; ok {
		found = true
	}
	if !found {
		writeAdmissionJSON(w, &wingwire.CancelLeaseAck{})
		return
	}
	const reason = "cancelled via sparkwing runs cancel"
	idsSet := map[string]bool{string(wingwire.HolderRef(request.RunID).RunID()): true}
	for _, target := range targets {
		idsSet[string(wingwire.HolderRef(target.runID).RunID())] = true
	}
	ids := make([]string, 0, len(idsSet))
	for id := range idsSet {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if d.cfg.Runs != nil {
		d.mu.Unlock()
		err := d.cfg.Runs.FinalizeCancelledRuns(ids, reason)
		d.mu.Lock()
		if err != nil {
			admissionHTTPFailure(w, err)
			return
		}
	}
	if err := d.cancelAdmissionRunLocked(request.RunID, reason); err != nil {
		admissionHTTPFailure(w, err)
		return
	}
	if err := d.commitAdmissionLocked(); err != nil {
		admissionHTTPFailure(w, err)
		return
	}
	writeAdmissionJSON(w, &wingwire.CancelLeaseAck{Found: true})
}

func (d *Daemon) reapAdmission() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.reapAdmissionLocked()
}

func (d *Daemon) reapAdmissionLocked() error {
	birth := d.processBirth
	if birth == nil {
		birth = admissionProcessBirth
	}
	changed := false
	type birthResult struct {
		birth string
		err   error
	}
	observed := map[int]birthResult{}
	var events []admission.Event
	for _, ticket := range d.admissionTickets {
		if !ticket.Active {
			continue
		}
		observation, ok := observed[ticket.Input.RootPID]
		if !ok {
			observation.birth, observation.err = birth(ticket.Input.RootPID)
			observed[ticket.Input.RootPID] = observation
		}
		actual, err := observation.birth, observation.err
		if err != nil && !errors.Is(err, procgroup.ErrProcessAbsent) {
			continue
		}
		if err == nil && actual == ticket.Input.ProcessBirth {
			continue
		}
		ticket.Result.State = "cancelled"
		ticket.Result.Cancel = &wingwire.Cancel{RunID: string(ticket.Input.Request.RunID), Reason: "holder process exited or PID was reused"}
		ticket.Result.Grant = nil
		ticket.Result.Queued = nil
		released, err := d.releaseAdmissionTicketLocked(ticket)
		if err != nil {
			return err
		}
		events = append(events, released...)
		changed = true
	}
	if changed {
		d.routeLocked(events)
		return d.commitAdmissionLocked()
	}
	return nil
}

func (d *Daemon) admissionLivenessLoop(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.quit:
			return
		case <-tick.C:
			if err := d.reapAdmission(); err != nil {
				d.cfg.logf("process liveness: %v", err)
			}
		}
	}
}

func (d *Daemon) cancelCommittedRunAdmission(_ context.Context, runID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ledger == nil {
		return errors.New("machine admission is not initialized")
	}
	if err := d.cancelAdmissionRunLocked(runID, "run cancellation committed"); err != nil {
		return err
	}
	return d.commitAdmissionLocked()
}

func (d *Daemon) cancelAdmissionRunLocked(runID, reason string) error {
	targets := d.runConnsLocked(runID)
	d.recordCancelledRunLocked(runID)
	var events []admission.Event
	for _, target := range targets {
		ticket := d.admissionTickets[target.ticketID]
		if ticket == nil || !ticket.Active {
			continue
		}
		ticket.Result.State = "cancelled"
		ticket.Result.Cancel = &wingwire.Cancel{RunID: target.runID, Reason: reason}
		ticket.Result.Grant = nil
		ticket.Result.Queued = nil
		released, err := d.releaseAdmissionTicketLocked(ticket)
		if err != nil {
			return err
		}
		events = append(events, released...)
	}
	d.routeLocked(events)
	return nil
}

func (d *Daemon) processAdmissionHTTP(w http.ResponseWriter, r *http.Request, subject, id string, peer localhostwire.LocalPeerIdentity) {
	var request struct {
		PID          int    `json:"pid"`
		ProcessBirth string `json:"process_birth"`
	}
	if !decodeAdmissionHTTP(w, r, &request) {
		return
	}
	if request.PID <= 0 || request.ProcessBirth == "" {
		writeLoginFailure(w, &apierr.Error{Code: apierr.BadRequest, Message: "pid and process_birth are required"})
		return
	}
	if err := verifyAdmissionProcess(peer, request.PID, request.ProcessBirth); err != nil {
		writeLoginFailure(w, &apierr.Error{Code: apierr.Forbidden, Message: err.Error()})
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	ticket := d.admissionTickets[id]
	if ticket == nil || ticket.Subject != subject {
		writeLoginFailure(w, &apierr.Error{Code: apierr.NotFound, Message: "admission ticket not found"})
		return
	}
	if !ticket.Active || ticket.Result.State != "granted" {
		writeLoginFailure(w, &apierr.Error{Code: apierr.Conflict, Message: "admission lease is no longer granted"})
		return
	}
	ticket.MeasurementPID = request.PID
	ticket.MeasurementBirth = request.ProcessBirth
	ticket.participant.measurementPID = request.PID
	ticket.participant.measurementBirth = request.ProcessBirth
	if err := d.commitAdmissionLocked(); err != nil {
		admissionHTTPFailure(w, err)
		return
	}
	writeAdmissionJSON(w, ticket.Result)
}

type admissionResponseBuffer struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (b *admissionResponseBuffer) Header() http.Header { return b.header }
func (b *admissionResponseBuffer) WriteHeader(status int) {
	if b.status == 0 {
		b.status = status
	}
}

func (b *admissionResponseBuffer) Write(data []byte) (int, error) {
	if b.status == 0 {
		b.status = http.StatusOK
	}
	return b.body.Write(data)
}

func (b *admissionResponseBuffer) send(w http.ResponseWriter) {
	for key, values := range b.header {
		w.Header()[key] = values
	}
	if b.status == 0 {
		b.status = http.StatusOK
	}
	w.WriteHeader(b.status)
	if _, err := w.Write(b.body.Bytes()); err != nil {
		panic(http.ErrAbortHandler) //nolint:forbidigo // net/http aborts the incomplete response.
	}
}
