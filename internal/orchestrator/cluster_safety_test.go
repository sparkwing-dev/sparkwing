package orchestrator_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func buildClusterRunnerBackends() orchestrator.Backends {
	const ctrlURL = "http://controller.sparkwing.svc.cluster.local"
	stateClient := client.NewWithToken(ctrlURL, nil, "")
	return orchestrator.Backends{
		State:       stateClient,
		Logs:        nil,
		Concurrency: orchestrator.NewHTTPConcurrency(ctrlURL, nil, "", store.DefaultConcurrencyLease),
	}
}

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
