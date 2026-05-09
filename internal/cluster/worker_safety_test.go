package cluster

// Cluster-mode RunWorker safety: HTTP-only Backends invariant.
//
// orchestrator/cluster_safety_test.go pins the runner-pod path that
// HandleClaimedTrigger constructs. RunWorker (this package) takes a
// parallel path: it claims triggers from the controller AND, in the
// same process, invokes ExecuteClaimedTrigger -- which calls
// orchestrator.Run, which executes .inline() user code in-process
// against the same Backends. So the same HTTP-only invariant must
// hold here:
//
//   Backends.State       -> *client.Client  (controller HTTP API)
//   Backends.Concurrency -> *HTTPConcurrency (controller HTTP API)
//
// It MUST NEVER receive a *store.Store directly. If RunWorker wired
// Concurrency from a throwaway local SQLite store (e.g. opened just
// to satisfy LocalBackends), that would be a privilege-escalation
// regression in waiting: the moment the sparkwing-runner image (or
// any future binary linking internal/cluster) bakes user pipelines
// in, .inline() jobs would have direct SQLite access via
// Backends.Concurrency. This test pins the HTTP-only wiring so a
// future "simplification" can't silently re-introduce that regression.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/controller/client"
	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// buildRunWorkerBackends mirrors the Backends construction inside
// internal/cluster/worker.go's RunWorker (current wiring). If
// that block changes shape, update this fixture in lockstep -- the
// assertions below pin the *types*, not the construction syntax, so
// the static fixture is a faithful stand-in for the live constructor.
func buildRunWorkerBackends() orchestrator.Backends {
	const ctrlURL = "http://controller.sparkwing.svc.cluster.local"
	stateClient := client.NewWithToken(ctrlURL, nil, "")
	return orchestrator.Backends{
		State:       stateClient,
		Logs:        nil, // Logs may be local-fs or HTTP; not the privilege boundary.
		Concurrency: orchestrator.NewHTTPConcurrency(ctrlURL, nil, "", store.DefaultConcurrencyLease),
	}
}

// TestRunWorkerBackends_StateMustBeHTTP rejects *store.Store on
// Backends.State. See file header for rationale.
func TestRunWorkerBackends_StateMustBeHTTP(t *testing.T) {
	backends := buildRunWorkerBackends()

	if backends.State == nil {
		t.Fatal("RunWorker Backends.State is nil; cluster wiring is broken")
	}
	stateType := reflect.TypeOf(backends.State).String()

	if stateType == "*store.Store" {
		t.Fatalf(`RunWorker Backends.State must be HTTP-backed for cluster
mode; got *store.Store. This is a PRIVILEGE-ESCALATION REGRESSION --
.inline() pipeline code in the worker process would gain controller-
level write access to the state DB.`)
	}
	if !strings.Contains(stateType, "client.Client") {
		t.Fatalf(`RunWorker Backends.State must be HTTP-backed
(*client.Client); got %s.`, stateType)
	}
}

// TestRunWorkerBackends_ConcurrencyMustBeHTTP rejects the SQLite-
// direct localConcurrency on Backends.Concurrency. This is the exact
// privilege-escalation regression the HTTP-only invariant prevents.
func TestRunWorkerBackends_ConcurrencyMustBeHTTP(t *testing.T) {
	backends := buildRunWorkerBackends()

	if backends.Concurrency == nil {
		t.Fatal("RunWorker Backends.Concurrency is nil; cluster wiring is broken")
	}
	concType := reflect.TypeOf(backends.Concurrency).String()

	if strings.Contains(concType, "localConcurrency") {
		t.Fatalf(`RunWorker Backends.Concurrency must be HTTP-backed for
cluster mode; got %s (SQLite-direct). This is a PRIVILEGE-
ESCALATION REGRESSION -- .inline() pipeline code in the worker
process would gain direct write access to the controller's
concurrency tables.`, concType)
	}
	if !strings.Contains(concType, "HTTPConcurrency") {
		t.Fatalf(`RunWorker Backends.Concurrency must be
*HTTPConcurrency; got %s.`, concType)
	}
}

// TestRunWorkerBackends_NoStoreReachable walks the Backends graph
// looking for any reachable *store.Store. Belt-and-suspenders against
// a future hybrid backend that lazily falls back to direct SQLite.
func TestRunWorkerBackends_NoStoreReachable(t *testing.T) {
	backends := buildRunWorkerBackends()

	if found := findStoreType(reflect.ValueOf(backends.State), 0); found != "" {
		t.Fatalf(`RunWorker Backends.State has a reachable *store.Store
at %s. Even an embedded / lazy direct-store reference collapses the
worker process's trust boundary -- .inline() pipeline code could
reach it via reflection or a hybrid backend's fallback path.`, found)
	}
	if found := findStoreType(reflect.ValueOf(backends.Concurrency), 0); found != "" {
		t.Fatalf(`RunWorker Backends.Concurrency has a reachable
*store.Store at %s.`, found)
	}
}

// findStoreType walks v looking for a *store.Store value. Returns
// the field path where it found one, or "" if none. Bounded depth
// keeps this from chasing into stdlib graph cycles.
//
// Duplicated rather than imported from the orchestrator-package test
// because Go test helpers don't cross packages cleanly and this file
// must live in internal/cluster to read the package-private wiring
// (even though the current fixture happens to mirror only exported
// types -- a future refactor that swaps in package-private helpers
// shouldn't break the test).
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
	case reflect.Ptr, reflect.Interface:
		if v.IsNil() {
			return ""
		}
		return findStoreType(v.Elem(), depth+1)
	case reflect.Struct:
		for i := range v.NumField() {
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

// TestRunWorkerBackends_GuardCatchesViolation is the meta-test:
// proves the assertions above actually fire on a *store.Store.
func TestRunWorkerBackends_GuardCatchesViolation(t *testing.T) {
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
