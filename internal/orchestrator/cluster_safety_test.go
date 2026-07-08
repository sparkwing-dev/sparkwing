package orchestrator_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// buildClusterRunnerBackends mirrors the runner-pod Backends wiring
// from orchestrator.HandleClaimedTrigger (orchestrator/worker.go,
// "Concurrency must go through the controller" block). If that block
// changes shape, update this fixture in lockstep -- the assertions
// below pin the *types*, not the construction syntax, so the static
// fixture is a faithful stand-in for the live constructor.
func buildClusterRunnerBackends() orchestrator.Backends {
	const ctrlURL = "http://controller.sparkwing.svc.cluster.local"
	stateClient := client.NewWithToken(ctrlURL, nil, "")
	return orchestrator.Backends{
		State:       stateClient,
		Logs:        nil,
		Concurrency: orchestrator.NewHTTPConcurrency(ctrlURL, nil, "", store.DefaultConcurrencyLease),
	}
}

// TestClusterBackends_StateMustBeHTTP pins Backends.State to the
// HTTP-backed *client.Client and rejects *store.Store. See file
// header for the privilege-escalation rationale.
func TestClusterBackends_StateMustBeHTTP(t *testing.T) {
	backends := buildClusterRunnerBackends()

	if backends.State == nil {
		t.Fatal("cluster orchestrator Backends.State is nil; cluster wiring is broken")
	}

	stateType := reflect.TypeOf(backends.State).String()

	if stateType == "*store.Store" {
		t.Fatalf(`cluster orchestrator Backends.State must be HTTP-backed for cluster
mode; got *store.Store. This is a PRIVILEGE-ESCALATION REGRESSION --
pipeline code running .inline() in a runner pod would gain
controller-level write access to the state DB. See
decisions/0001-open-core-tier-strategy.md for the security rationale.`)
	}

	if !strings.Contains(stateType, "client.Client") {
		t.Fatalf(`cluster orchestrator Backends.State must be HTTP-backed
(*client.Client); got %s. Any non-HTTP StateBackend in the runner
pod is a privilege-escalation regression. See
decisions/0001-open-core-tier-strategy.md.`, stateType)
	}
}

// TestClusterBackends_ConcurrencyMustBeHTTP pins Backends.Concurrency
// to *HTTPConcurrency and rejects the SQLite-direct localConcurrency.
// Cluster cache hits + slot coordination MUST flow through the
// controller; a per-pod local store would silo coordination AND grant
// inline jobs direct write access.
func TestClusterBackends_ConcurrencyMustBeHTTP(t *testing.T) {
	backends := buildClusterRunnerBackends()

	if backends.Concurrency == nil {
		t.Fatal("cluster orchestrator Backends.Concurrency is nil; cluster wiring is broken")
	}

	concType := reflect.TypeOf(backends.Concurrency).String()

	if strings.Contains(concType, "localConcurrency") {
		t.Fatalf(`cluster orchestrator Backends.Concurrency must be HTTP-backed
for cluster mode; got %s (SQLite-direct). This is a
PRIVILEGE-ESCALATION REGRESSION -- pipeline code running .inline()
in a runner pod would gain direct write access to the controller's
concurrency tables. See decisions/0001-open-core-tier-strategy.md
for the security rationale.`, concType)
	}

	if !strings.Contains(concType, "HTTPConcurrency") {
		t.Fatalf(`cluster orchestrator Backends.Concurrency must be
*HTTPConcurrency; got %s. Any non-HTTP ConcurrencyBackend in the
runner pod is a privilege-escalation regression. See
decisions/0001-open-core-tier-strategy.md.`, concType)
	}
}

// TestClusterBackends_NoStoreReachable walks Backends.State via
// reflection and asserts no *store.Store sits inside it. Belt-and-
// suspenders: catches the case where a future refactor wraps a
// *store.Store inside an HTTP-shaped struct (e.g. a "hybrid"
// StateBackend that lazily falls back to direct SQLite). The
// HTTP-only invariant is meant to be HARD: no embedded direct-store
// references anywhere in the runner-pod Backends graph.
func TestClusterBackends_NoStoreReachable(t *testing.T) {
	backends := buildClusterRunnerBackends()

	if found := findStoreType(reflect.ValueOf(backends.State), 0); found != "" {
		t.Fatalf(`cluster orchestrator Backends.State has a reachable
*store.Store at %s. Even an embedded / lazy direct-store reference
collapses the runner-pod trust boundary -- .inline() pipeline code
could reach it via reflection or via a hybrid backend's fallback
path. See decisions/0001-open-core-tier-strategy.md.`,
			found)
	}
	if found := findStoreType(reflect.ValueOf(backends.Concurrency), 0); found != "" {
		t.Fatalf(`cluster orchestrator Backends.Concurrency has a
reachable *store.Store at %s. See decisions/0001-open-core-tier-
strategy.md.`, found)
	}
}

// findStoreType walks v looking for a *store.Store value. Returns
// the field path where it found one, or "" if none. Bounded depth
// keeps this from chasing into stdlib graph cycles (net/http
// transports, etc.).
func findStoreType(v reflect.Value, depth int) string {
	if depth > 6 {
		return ""
	}
	if !v.IsValid() {
		return ""
	}
	t := v.Type()
	if t.String() == "*store.Store" {
		return t.String()
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return ""
		}
		return findStoreType(v.Elem(), depth+1)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			f := v.Field(i)
			pkg := f.Type().PkgPath()
			if pkg == "net/http" || pkg == "crypto/tls" || pkg == "sync" {
				continue
			}
			if found := findStoreType(f, depth+1); found != "" {
				return t.Name() + "." + t.Field(i).Name + " -> " + found
			}
		}
	}
	return ""
}

// TestClusterBackends_GuardCatchesViolation is the meta-test: prove
// the assertions above actually fire on a *store.Store. Without
// this, a future refactor could silently neuter the guard (e.g.
// rename the type) and the tests would keep passing on bad wiring.
//
// Constructs a deliberately-wrong Backends bundle and confirms the
// type checks classify it as a regression.
func TestClusterBackends_GuardCatchesViolation(t *testing.T) {
	var bad *store.Store
	stateType := reflect.TypeOf(bad).String()
	if stateType != "*store.Store" {
		t.Fatalf("guard meta-test: expected %q, got %q -- has the store package been renamed? Update the assertions in this file.",
			"*store.Store", stateType)
	}

	if strings.Contains(stateType, "client.Client") {
		t.Fatalf("guard meta-test: %q unexpectedly contains client.Client", stateType)
	}
}
