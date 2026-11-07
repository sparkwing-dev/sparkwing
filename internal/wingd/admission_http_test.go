package wingd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/journal"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

type admissionMemoryStore struct {
	mu   sync.Mutex
	data []byte
	fail bool
}

func (s *admissionMemoryStore) LoadMachineAdmission(context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return bytes.Clone(s.data), nil
}

func (s *admissionMemoryStore) SaveMachineAdmission(_ context.Context, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return errors.New("disk unavailable")
	}
	s.data = bytes.Clone(data)
	return nil
}

type admissionTestSampler struct{}

func (admissionTestSampler) Sample() (HostStat, error) {
	return HostStat{TotalCores: 2, TotalMemoryBytes: 8 << 30}, nil
}

func newHTTPAdmissionTest(t *testing.T, persistence MachineAdmissionStore) *Daemon {
	t.Helper()
	d, err := New(Config{Home: t.TempDir(), Sampler: admissionTestSampler{}, AdmissionStore: persistence, HeadroomFraction: -1})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.initLedger(); err != nil {
		t.Fatal(err)
	}
	return d
}

func admissionHTTPRequest(t *testing.T, ctx context.Context, method, path string, input any) *http.Request {
	t.Helper()
	var body bytes.Buffer
	if input != nil {
		if err := json.NewEncoder(&body).Encode(input); err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequestWithContext(ctx, method, admissionHTTPPath+path, &body)
	request.Header.Set("Content-Type", "application/json")
	peer, err := controllerPeerIdentity()
	if err != nil {
		t.Fatal(err)
	}
	request = request.WithContext(context.WithValue(localhostwire.WithLocalPeer(ctx, peer), socketPeerContextKey{}, true))
	return request
}

func admissionHTTPCall(t *testing.T, d *Daemon, method, path string, input any) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	d.admissionHTTP(http.NotFoundHandler()).ServeHTTP(response, admissionHTTPRequest(t, t.Context(), method, path, input))
	return response
}

func admissionResponse(t *testing.T, response *httptest.ResponseRecorder) AdmissionHTTPState {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("admission HTTP %d: %s", response.Code, response.Body.String())
	}
	var state AdmissionHTTPState
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func admissionTestInput(t *testing.T, name string, cores float64) AdmissionHTTPRequest {
	t.Helper()
	birth, err := procgroup.ProcessBirth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	return AdmissionHTTPRequest{RequestKey: name, RootPID: os.Getpid(), ProcessBirth: birth, Request: wingwire.AdmissionRequest{RunID: wingwire.HolderRef(name), PID: os.Getpid(), Resources: wingwire.HostResources{Cores: cores}, CostSource: wingwire.CostSourcePin}}
}

func TestMachineAdmissionRestartMidQueuePreservesOrderAndGrants(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	owned, err := teststore.Open(t, path)
	if err != nil {
		t.Fatal(err)
	}
	d := newHTTPAdmissionTest(t, owned)
	first := admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests", admissionTestInput(t, "holder", 2)))
	second := admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests", admissionTestInput(t, "wait-1", 2)))
	third := admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests", admissionTestInput(t, "wait-2", 2)))
	if first.Grant == nil || second.Queued == nil || third.Queued == nil || second.Arrival >= third.Arrival {
		t.Fatalf("initial admissions: %+v %+v %+v", first, second, third)
	}
	if err := owned.Close(); err != nil {
		t.Fatal(err)
	}
	owned, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer owned.Close()
	restarted := newHTTPAdmissionTest(t, owned)
	admissionResponse(t, admissionHTTPCall(t, restarted, http.MethodDelete, "/requests/"+first.ID, nil))
	promoted := admissionResponse(t, admissionHTTPCall(t, restarted, http.MethodGet, "/requests/"+second.ID, nil))
	queued := admissionResponse(t, admissionHTTPCall(t, restarted, http.MethodGet, "/requests/"+third.ID, nil))
	if promoted.Grant == nil || queued.Queued == nil || queued.Arrival != third.Arrival {
		t.Fatalf("after restart/release: %+v %+v", promoted, queued)
	}
	admissionResponse(t, admissionHTTPCall(t, restarted, http.MethodDelete, "/requests/"+second.ID, nil))
	if got := admissionResponse(t, admissionHTTPCall(t, restarted, http.MethodGet, "/requests/"+third.ID, nil)); got.Grant == nil {
		t.Fatalf("FIFO successor: %+v", got)
	}
}

func TestMachineAdmissionRequestReleaseIdempotencyAndCommitFailure(t *testing.T) {
	persistence := &admissionMemoryStore{}
	d := newHTTPAdmissionTest(t, persistence)
	input := admissionTestInput(t, "idempotent", 2)
	first := admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests", input))
	replay := admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests", input))
	if first.ID != replay.ID || first.Grant.LeaseToken != replay.Grant.LeaseToken || len(d.ledger.Snapshot().Leases) != 1 {
		t.Fatal("retry created another lease")
	}
	input.Request.Resources.Cores = 1
	if response := admissionHTTPCall(t, d, http.MethodPost, "/requests", input); response.Code != http.StatusConflict {
		t.Fatalf("changed retry %d", response.Code)
	}
	persistence.fail = true
	if response := admissionHTTPCall(t, d, http.MethodDelete, "/requests/"+first.ID, nil); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("failed commit %d", response.Code)
	}
	if got := admissionResponse(t, admissionHTTPCall(t, d, http.MethodGet, "/requests/"+first.ID, nil)); got.Grant == nil || len(d.ledger.Snapshot().Leases) != 1 {
		t.Fatalf("failed release escaped rollback: %+v", got)
	}
	persistence.fail = false
	for range 2 {
		if got := admissionResponse(t, admissionHTTPCall(t, d, http.MethodDelete, "/requests/"+first.ID, nil)); got.State != "released" {
			t.Fatalf("release %+v", got)
		}
	}
	input.Request.Resources.Cores = 2
	if got := admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests", input)); got.State != "released" {
		t.Fatal("retry resurrected a released request")
	}
}

func TestMachineAdmissionLongPollWakesAndDroppedPollKeepsArrival(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := newHTTPAdmissionTest(t, &admissionMemoryStore{})
		held := admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests", admissionTestInput(t, "held", 2)))
		waiting := admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests", admissionTestInput(t, "waiting", 2)))
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			defer close(done)
			response := httptest.NewRecorder()
			d.admissionHTTP(http.NotFoundHandler()).ServeHTTP(response, admissionHTTPRequest(t, ctx, http.MethodGet, "/requests/"+waiting.ID+"?wait=true", nil))
		}()
		synctest.Wait()
		cancel()
		<-done
		replay := admissionResponse(t, admissionHTTPCall(t, d, http.MethodGet, "/requests/"+waiting.ID, nil))
		if replay.Arrival != waiting.Arrival || replay.Queued == nil {
			t.Fatal("dropped poll lost queue position")
		}
		promoted := make(chan AdmissionHTTPState, 1)
		go func() {
			promoted <- admissionResponse(t, admissionHTTPCall(t, d, http.MethodGet, "/requests/"+waiting.ID+"?wait=true", nil))
		}()
		synctest.Wait()
		start := time.Now()
		admissionResponse(t, admissionHTTPCall(t, d, http.MethodDelete, "/requests/"+held.ID, nil))
		got := <-promoted
		if got.Grant == nil || time.Since(start) != 0 {
			t.Fatalf("poll did not wake at commit: %+v", got)
		}
	})
}

func TestMachineAdmissionLongPollReturnsAfterTwentySeconds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := newHTTPAdmissionTest(t, &admissionMemoryStore{})
		admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests", admissionTestInput(t, "held", 2)))
		queued := admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests", admissionTestInput(t, "queued", 2)))
		start := time.Now()
		got := admissionResponse(t, admissionHTTPCall(t, d, http.MethodGet, "/requests/"+queued.ID+"?wait=true", nil))
		if time.Since(start) != 20*time.Second || got.Arrival != queued.Arrival || got.Queued == nil {
			t.Fatalf("poll elapsed=%s state=%+v", time.Since(start), got)
		}
	})
}

func TestMachineAdmissionProcessLiveness(t *testing.T) {
	for _, test := range []struct {
		name  string
		birth string
		err   error
		freed bool
	}{{"dead", "", procgroup.ErrProcessAbsent, true}, {"reused", "different", nil, true}, {"inspection-error", "", errors.New("permission denied"), false}} {
		t.Run(test.name, func(t *testing.T) {
			d := newHTTPAdmissionTest(t, &admissionMemoryStore{})
			held := admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests", admissionTestInput(t, "held", 2)))
			waiting := admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests", admissionTestInput(t, "waiting", 2)))
			d.mu.Lock()
			d.admissionTickets[waiting.ID].Input.RootPID = -7
			d.processBirth = func(pid int) (string, error) {
				if pid == -7 {
					return waitingBirth(t), nil
				}
				return test.birth, test.err
			}
			d.mu.Unlock()
			if err := d.reapAdmission(); err != nil {
				t.Fatal(err)
			}
			got := admissionResponse(t, admissionHTTPCall(t, d, http.MethodGet, "/requests/"+waiting.ID, nil))
			if (got.Grant != nil) != test.freed {
				t.Fatalf("capacity not reconciled: %+v holder=%s", got, held.ID)
			}
		})
	}
}

func waitingBirth(t *testing.T) string {
	t.Helper()
	birth, err := procgroup.ProcessBirth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	return birth
}

func TestMachineAdmissionLeaseWatchMeasuresCommitToCancel(t *testing.T) {
	owned, err := teststore.Open(t, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owned.Close(); err != nil {
			t.Error(err)
		}
	})
	persistence := &admissionTimedStore{MachineAdmissionStore: owned}
	d := newHTTPAdmissionTest(t, persistence)
	held := admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests", admissionTestInput(t, "watched", 2)))
	result := make(chan AdmissionHTTPState, 1)
	go func() {
		result <- admissionResponse(t, admissionHTTPCall(t, d, http.MethodGet, "/requests/"+held.ID+"/watch?wait=true", nil))
	}()
	response := admissionHTTPCall(t, d, http.MethodPost, "/cancel", wingwire.CancelLease{RunID: "watched"})
	if response.Code != http.StatusOK {
		t.Fatalf("cancel: %s", response.Body.String())
	}
	got := <-result
	persistence.mu.Lock()
	latency := time.Since(persistence.committed)
	persistence.mu.Unlock()
	t.Logf("cancel commit to lease watch: %s", latency)
	if got.Cancel == nil {
		t.Fatalf("cancel notification %+v after %s", got, latency)
	}
}

func TestMachineAdmissionRejectsBrowserPeerAndWrongProcessBirth(t *testing.T) {
	d := newHTTPAdmissionTest(t, &admissionMemoryStore{})
	request := admissionHTTPRequest(t, t.Context(), http.MethodPost, "/requests", admissionTestInput(t, "browser", 1))
	peer, _ := controllerPeerIdentity()
	request = request.WithContext(localhostwire.WithLocalPeer(t.Context(), peer))
	response := httptest.NewRecorder()
	d.admissionHTTP(http.NotFoundHandler()).ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("cookie authority accepted: %d", response.Code)
	}
	input := admissionTestInput(t, "wrong-birth", 1)
	input.ProcessBirth = "reused"
	response = admissionHTTPCall(t, d, http.MethodPost, "/requests", input)
	if response.Code != http.StatusForbidden {
		t.Fatalf("reused PID accepted: %d", response.Code)
	}
}

func TestMachineAdmissionPolicyEvictionWakesLeaseWatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := newHTTPAdmissionTest(t, &admissionMemoryStore{})
		input := admissionTestInput(t, "old", 0)
		input.Request.SemaphoresOnly = true
		input.Request.Semaphores = []wingwire.SemaphoreClaim{{Name: "exclusive", Capacity: 1, Cost: 1, Policy: wingwire.PolicyQueue}}
		held := admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests", input))
		evicted := make(chan AdmissionHTTPState, 1)
		go func() {
			evicted <- admissionResponse(t, admissionHTTPCall(t, d, http.MethodGet, "/requests/"+held.ID+"/watch?wait=true", nil))
		}()
		synctest.Wait()
		next := input
		next.RequestKey = "new"
		next.Request.RunID = "new"
		next.Request.Semaphores[0].Policy = wingwire.PolicyCancelOthers
		if got := admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests", next)); got.Grant == nil {
			t.Fatalf("superseder: %+v", got)
		}
		if got := <-evicted; got.Evicted == nil || got.Evicted.SupersededBy != "new" {
			t.Fatalf("eviction watch: %+v", got)
		}
	})
}

func TestMachineAdmissionCancellationByKeySurvivesRestart(t *testing.T) {
	persistence := &admissionMemoryStore{}
	d := newHTTPAdmissionTest(t, persistence)
	input := admissionTestInput(t, "cancel-key", 2)
	admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests", input))
	admissionResponse(t, admissionHTTPCall(t, d, http.MethodDelete, "/requests/by-key/"+input.RequestKey, nil))
	d = newHTTPAdmissionTest(t, persistence)
	if got := admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests", input)); got.State != "released" {
		t.Fatalf("cancel key replay: %+v", got)
	}
}

func TestMachineAdmissionOrphanWatchAndTerminalReplay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		persistence := &admissionMemoryStore{}
		d := newHTTPAdmissionTest(t, persistence)
		parentInput := admissionTestInput(t, "ancestor", 0)
		parentInput.Request.SemaphoresOnly = true
		childInput := admissionTestInput(t, "dependent", 0)
		childInput.Request.SemaphoresOnly = true
		parent := admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests", parentInput))
		child := admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests", childInput))
		d.mu.Lock()
		err := d.ledger.Depend("dependent", []admission.LeaseID{d.admissionTickets[parent.ID].LeaseID})
		if err == nil {
			err = d.commitAdmissionLocked()
		}
		d.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		observed := make(chan AdmissionHTTPState, 1)
		go func() {
			observed <- admissionResponse(t, admissionHTTPCall(t, d, http.MethodGet, "/requests/"+child.ID+"/watch?wait=true", nil))
		}()
		synctest.Wait()
		admissionResponse(t, admissionHTTPCall(t, d, http.MethodDelete, "/requests/"+parent.ID, nil))
		got := <-observed
		if got.Cancel == nil || got.Grant != nil {
			t.Fatalf("orphan retained authority: %+v", got)
		}
		restarted := newHTTPAdmissionTest(t, persistence)
		replay := admissionResponse(t, admissionHTTPCall(t, restarted, http.MethodPost, "/requests", childInput))
		if replay.Grant != nil || replay.Cancel == nil {
			t.Fatalf("orphan replay granted authority: %+v", replay)
		}
	})
}

func TestMachineAdmissionPollReturnsPositionChangeAndIgnoresEmptyHint(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := newHTTPAdmissionTest(t, &admissionMemoryStore{})
		holder := admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests", admissionTestInput(t, "holder", 2)))
		admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests", admissionTestInput(t, "first", 2)))
		queued := admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests", admissionTestInput(t, "second", 2)))
		observed := make(chan AdmissionHTTPState, 1)
		go func() {
			observed <- admissionResponse(t, admissionHTTPCall(t, d, http.MethodGet, "/requests/"+queued.ID+"?wait=true", nil))
		}()
		synctest.Wait()
		d.mu.Lock()
		close(d.admissionChanged)
		d.admissionChanged = make(chan struct{})
		d.mu.Unlock()
		synctest.Wait()
		select {
		case got := <-observed:
			t.Fatalf("hint changed durable outcome: %+v", got)
		default:
		}
		admissionResponse(t, admissionHTTPCall(t, d, http.MethodDelete, "/requests/"+holder.ID, nil))
		got := <-observed
		if got.Queued == nil || got.Queued.Position != 1 || got.Arrival != queued.Arrival {
			t.Fatalf("position wake: %+v", got)
		}
	})
}

func TestMachineAdmissionCancelByKeyBeforeRequest(t *testing.T) {
	persistence := &admissionMemoryStore{}
	d := newHTTPAdmissionTest(t, persistence)
	input := admissionTestInput(t, "lost-post-race", 2)
	admissionResponse(t, admissionHTTPCall(t, d, http.MethodDelete, "/requests/by-key/"+input.RequestKey, nil))
	d = newHTTPAdmissionTest(t, persistence)
	got := admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests", input))
	if got.State != "released" || len(d.ledger.Snapshot().Leases) != 0 {
		t.Fatalf("late POST escaped cancellation: %+v", got)
	}
}

func TestMachineAdmissionOldPinNamesBothVersionsAndUpgrade(t *testing.T) {
	for _, operation := range []string{"admission_request", "queue_state", "set_priority", "cancel_lease", "drain_request"} {
		t.Run(operation, func(t *testing.T) {
			d := newHTTPAdmissionTest(t, &admissionMemoryStore{})
			d.cfg.Version = "v0.70.0"
			d.journal = journal.NewWriter(d.layout.dir, 1, nil)
			server, client := net.Pipe()
			done := make(chan struct{})
			go func() { defer close(done); d.refuseLegacyAdmission(server) }()
			defer client.Close()
			encoder, decoder := json.NewEncoder(client), json.NewDecoder(client)
			if err := encoder.Encode(map[string]any{"type": "hello", "payload": map[string]any{"protocol_major": 3, "binary_version": "v0.69.1"}}); err != nil {
				t.Fatal(err)
			}
			var ack map[string]any
			if err := decoder.Decode(&ack); err != nil {
				t.Fatal(err)
			}
			if err := encoder.Encode(map[string]any{"type": operation, "payload": map[string]any{"run_id": "old-pin"}}); err != nil {
				t.Fatal(err)
			}
			var refusal struct {
				Type    string `json:"type"`
				Payload struct {
					Reason string `json:"type"`
				} `json:"payload"`
			}
			if err := decoder.Decode(&refusal); err != nil {
				t.Fatal(err)
			}
			if refusal.Type != "unsupported" {
				t.Fatalf("legacy operation returned %q", refusal.Type)
			}
			for _, want := range []string{"v0.69.1", "v0.70.0", "sparkwing pipeline upgrade", "sparkwing-runner", "runner.local_admission: false"} {
				if !strings.Contains(refusal.Payload.Reason, want) {
					t.Fatalf("refusal missing %q: %s", want, refusal.Payload.Reason)
				}
			}
			<-done
			d.journal.Close()
			records, _, err := journal.ReadWithStats(d.layout.dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(records) != 1 || records[0].Kind != "handshake_refused" || records[0].Data["peer_version"] != "v0.69.1" {
				t.Fatalf("refusal left no journal record for daemon events: %+v", records)
			}
			if len(d.ledger.Snapshot().Leases) != 0 {
				t.Fatal("legacy peer acquired authority")
			}
		})
	}
}

type admissionTimedStore struct {
	MachineAdmissionStore
	mu        sync.Mutex
	committed time.Time
}

func (s *admissionTimedStore) SaveMachineAdmission(ctx context.Context, data []byte) error {
	if err := s.MachineAdmissionStore.SaveMachineAdmission(ctx, data); err != nil {
		return err
	}
	s.mu.Lock()
	s.committed = time.Now()
	s.mu.Unlock()
	return nil
}

func TestMachineAdmissionRejectsEmptyCancellationKey(t *testing.T) {
	persistence := &admissionMemoryStore{}
	d := newHTTPAdmissionTest(t, persistence)
	response := admissionHTTPCall(t, d, http.MethodDelete, "/requests/by-key/", nil)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("empty key %d: %s", response.Code, response.Body.String())
	}
	restarted := newHTTPAdmissionTest(t, persistence)
	if len(restarted.admissionTickets) != 0 {
		t.Fatal("empty key committed invalid state")
	}
}

func TestMachineAdmissionQueuedCancellationAdvancesDurableRevision(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		persistence := &admissionMemoryStore{}
		d := newHTTPAdmissionTest(t, persistence)
		admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests", admissionTestInput(t, "holder", 2)))
		first := admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests", admissionTestInput(t, "first", 2)))
		second := admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests", admissionTestInput(t, "second", 2)))
		path := "/requests/" + second.ID + "?wait=true&after=" + strconv.FormatUint(second.Revision, 10)
		observed := make(chan AdmissionHTTPState, 1)
		go func() { observed <- admissionResponse(t, admissionHTTPCall(t, d, http.MethodGet, path, nil)) }()
		synctest.Wait()
		admissionResponse(t, admissionHTTPCall(t, d, http.MethodDelete, "/requests/"+first.ID, nil))
		got := <-observed
		if got.Queued == nil || got.Queued.Position != 1 || got.Revision <= second.Revision {
			t.Fatalf("queued cancel did not advance position/revision: %+v", got)
		}
		restarted := newHTTPAdmissionTest(t, persistence)
		start := time.Now()
		replay := admissionResponse(t, admissionHTTPCall(t, restarted, http.MethodGet, path, nil))
		if replay.Queued == nil || replay.Queued.Position != 1 || time.Since(start) != 0 {
			t.Fatalf("reconnect lost committed queue change: %+v", replay)
		}
	})
}

func TestMachineAdmissionProcessRegistrationIsDurableAndRollsBack(t *testing.T) {
	persistence := &admissionMemoryStore{}
	d := newHTTPAdmissionTest(t, persistence)
	input := admissionTestInput(t, "measured", 1)
	held := admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests", input))
	request := map[string]any{"pid": input.RootPID, "process_birth": input.ProcessBirth}
	persistence.fail = true
	response := admissionHTTPCall(t, d, http.MethodPost, "/requests/"+held.ID+"/process", request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("failed measurement commit %d", response.Code)
	}
	if d.admissionTickets[held.ID].MeasurementPID != 0 {
		t.Fatal("failed commit changed measurement identity")
	}
	persistence.fail = false
	admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests/"+held.ID+"/process", request))
	restarted := newHTTPAdmissionTest(t, persistence)
	ticket := restarted.admissionTickets[held.ID]
	if ticket.MeasurementPID != input.RootPID || ticket.MeasurementBirth != input.ProcessBirth || ticket.participant.samplePID() != input.RootPID {
		t.Fatalf("measurement identity lost on restart: %+v", ticket)
	}
	request["process_birth"] = "reused"
	response = admissionHTTPCall(t, restarted, http.MethodPost, "/requests/"+held.ID+"/process", request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("PID reuse accepted: %d", response.Code)
	}
}

func TestMachineAdmissionLeaseWatchCancellationWakesAtCommit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := newHTTPAdmissionTest(t, &admissionMemoryStore{})
		held := admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests", admissionTestInput(t, "watched", 2)))
		observed := make(chan AdmissionHTTPState, 1)
		go func() {
			observed <- admissionResponse(t, admissionHTTPCall(t, d, http.MethodGet, "/requests/"+held.ID+"/watch?wait=true&after="+strconv.FormatUint(held.Revision, 10), nil))
		}()
		synctest.Wait()
		start := time.Now()
		if err := d.cancelCommittedRunAdmission(t.Context(), "watched"); err != nil {
			t.Fatal(err)
		}
		got := <-observed
		if got.Cancel == nil || time.Since(start) != 0 {
			t.Fatalf("watch missed cancellation commit: %+v after %s", got, time.Since(start))
		}
	})
}

func TestMachineAdmissionCancelAllowsSynchronousStoreNotification(t *testing.T) {
	d := newHTTPAdmissionTest(t, &admissionMemoryStore{})
	input := admissionTestInput(t, "job-key", 1)
	input.Request.RunID = wingwire.NodeHostParticipant("run", "job")
	held := admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests", input))
	d.cfg.Runs = &FuncRunStore{FinalizeCancelled: func(ids []string, _ string) error {
		if len(ids) != 1 || ids[0] != "run" {
			t.Fatalf("store cancellation received participant IDs: %v", ids)
		}
		return d.cancelCommittedRunAdmission(t.Context(), "run")
	}}
	response := admissionHTTPCall(t, d, http.MethodPost, "/cancel", wingwire.CancelLease{RunID: "run"})
	if response.Code != http.StatusOK {
		t.Fatalf("cancel notification failed: %s", response.Body.String())
	}
	got := admissionResponse(t, admissionHTTPCall(t, d, http.MethodGet, "/requests/"+held.ID, nil))
	if got.Cancel == nil || got.Grant != nil {
		t.Fatalf("store callback retained authority: %+v", got)
	}
}

func TestMachineAdmissionCancelledRemoteRunRejectsNewParticipant(t *testing.T) {
	d := newHTTPAdmissionTest(t, &admissionMemoryStore{})
	run := string(wingwire.RemoteRunID("https://controller.test", "run"))
	input := admissionTestInput(t, "first", 1)
	input.Request.RunID = wingwire.NodeHostParticipant(run, "first")
	admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests", input))
	response := admissionHTTPCall(t, d, http.MethodPost, "/cancel", wingwire.CancelLease{RunID: run})
	if response.Code != http.StatusOK {
		t.Fatalf("cancel failed: %s", response.Body.String())
	}
	input.RequestKey = "second"
	input.Request.RunID = wingwire.NodeHostParticipant(run, "second")
	got := admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests", input))
	if got.Evicted == nil || got.Evicted.Key != "cancelled" || got.Grant != nil {
		t.Fatalf("cancelled run reacquired: %+v", got)
	}
}

func TestMachineAdmissionBoundsTerminalReceiptsAcrossRestarts(t *testing.T) {
	persistence := &admissionMemoryStore{}
	d := newHTTPAdmissionTest(t, persistence)
	now := time.Now()
	d.cfg.Now = func() time.Time { return now }
	held := admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests", admissionTestInput(t, "held", 2)))
	queued := admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests", admissionTestInput(t, "queued", 2)))
	var latest AdmissionHTTPRequest
	var refused AdmissionHTTPState
	for i := range 512 {
		now = now.Add(time.Millisecond)
		latest = admissionTestInput(t, "burst-"+strconv.Itoa(i), 1)
		latest.Request.NonBlocking = true
		refused = admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests", latest))
		if refused.Evicted == nil || refused.Evicted.Key != "capacity" {
			t.Fatalf("burst refusal: %+v", refused)
		}
		if len(d.admissionTickets) > 258 || len(d.admissionKeys) > 258 {
			t.Fatalf("terminal history grew unbounded: tickets=%d keys=%d", len(d.admissionTickets), len(d.admissionKeys))
		}
	}
	restarted := newHTTPAdmissionTest(t, persistence)
	replay := admissionResponse(t, admissionHTTPCall(t, restarted, http.MethodPost, "/requests", latest))
	if replay.ID != refused.ID || replay.Evicted == nil {
		t.Fatalf("restart lost recent retry receipt: %+v", replay)
	}
	now = now.Add(2 * time.Minute)
	restarted.cfg.Now = func() time.Time { return now }
	admissionResponse(t, admissionHTTPCall(t, restarted, http.MethodDelete, "/requests/by-key/cancelled-before-post", nil))
	if len(restarted.admissionTickets) != 3 || len(restarted.admissionKeys) != 3 {
		t.Fatalf("expired receipts retained: tickets=%d keys=%d", len(restarted.admissionTickets), len(restarted.admissionKeys))
	}
	if got := admissionResponse(t, admissionHTTPCall(t, restarted, http.MethodGet, "/requests/"+queued.ID, nil)); got.Queued == nil || got.Arrival != queued.Arrival {
		t.Fatalf("pruning changed active waiter: %+v", got)
	}
	admissionResponse(t, admissionHTTPCall(t, restarted, http.MethodDelete, "/requests/"+held.ID, nil))
	if got := admissionResponse(t, admissionHTTPCall(t, restarted, http.MethodGet, "/requests/"+queued.ID, nil)); got.Grant == nil {
		t.Fatalf("pruning lost live authority: %+v", got)
	}
}

func TestMachineAdmissionBoundsCancellationTombstones(t *testing.T) {
	d := newHTTPAdmissionTest(t, &admissionMemoryStore{})
	for i := range 512 {
		admissionResponse(t, admissionHTTPCall(t, d, http.MethodDelete, "/requests/by-key/"+strconv.Itoa(i), nil))
		if len(d.admissionTickets) > 256 || len(d.admissionKeys) > 256 {
			t.Fatalf("cancelled request history grew unbounded: tickets=%d keys=%d", len(d.admissionTickets), len(d.admissionKeys))
		}
	}
}
