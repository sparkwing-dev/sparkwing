package wingd

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func handlerDaemon(t *testing.T, cores float64) *Daemon {
	t.Helper()
	return configuredHandlerDaemon(t, Config{}, cores)
}

func configuredHandlerDaemon(t *testing.T, cfg Config, cores float64) *Daemon {
	t.Helper()
	cfg.Home = t.TempDir()
	d, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	d.ledger, err = admission.New(admission.Config{TotalCores: cores})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func policyHTTP(t *testing.T, d *Daemon, method, path string, input any) *httptest.ResponseRecorder {
	t.Helper()
	var body []byte
	if input != nil {
		var err error
		body, err = json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	peer, err := controllerPeerIdentity()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(localhostwire.WithLocalPeer(t.Context(), peer), socketPeerContextKey{}, true)
	r = r.WithContext(ctx)
	w := httptest.NewRecorder()
	d.admissionHTTP(d.controllerHTTP(http.NotFoundHandler())).ServeHTTP(w, r)
	return w
}

func policyRequest(t *testing.T, d *Daemon, req wingwire.AdmissionRequest) AdmissionHTTPState {
	t.Helper()
	birth, err := procgroup.ProcessBirth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	input := AdmissionHTTPRequest{RequestKey: rand.Text(), Request: req, RootPID: os.Getpid(), ProcessBirth: birth}
	w := policyHTTP(t, d, http.MethodPost, admissionHTTPPath+"/requests", input)
	if w.Code != http.StatusOK {
		t.Fatalf("admit %s: HTTP %d: %s", req.RunID, w.Code, w.Body.String())
	}
	var state AdmissionHTTPState
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func policyRelease(t *testing.T, d *Daemon, id string) {
	t.Helper()
	w := policyHTTP(t, d, http.MethodDelete, admissionHTTPPath+"/requests/"+id, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("release: HTTP %d: %s", w.Code, w.Body.String())
	}
}

func leaseStateFor(t *testing.T, d *Daemon, requestID string) admission.LeaseState {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, lease := range d.ledger.Snapshot().Leases {
		if lease.RequestID == requestID {
			return lease
		}
	}
	t.Fatalf("no lease for %q", requestID)
	return admission.LeaseState{}
}

func TestSubLeaseFromAnUnrelatedRunKeepsNoOwner(t *testing.T) {
	d := handlerDaemon(t, 4)
	holder := policyRequest(t, d, wingwire.AdmissionRequest{RunID: "holder-run", Resources: wingwire.HostResources{Cores: 1}})
	if holder.Grant == nil {
		t.Fatalf("holder = %+v", holder)
	}
	other := policyRequest(t, d, wingwire.AdmissionRequest{RunID: "other-run", Resources: wingwire.HostResources{Cores: 1}})
	if other.Grant == nil {
		t.Fatalf("other = %+v", other)
	}
	sub := policyRequest(t, d, wingwire.AdmissionRequest{RunID: "other-run/plan-sems", OwnerRunID: "other-run", OwnerLeaseToken: holder.Grant.LeaseToken, SemaphoresOnly: true, SubLease: true, Semaphores: []wingwire.SemaphoreClaim{{Name: "grp", Capacity: 1, Cost: 1, Policy: wingwire.PolicyQueue}}})
	if sub.Grant == nil {
		t.Fatalf("sub = %+v", sub)
	}
	if got := leaseStateFor(t, d, "other-run/plan-sems").OwnerID; got != "" {
		t.Fatalf("sub-lease owner = %q, want none", got)
	}
}
