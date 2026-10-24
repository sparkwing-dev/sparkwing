package nodehost

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func nativeProcessesFixture(t *testing.T) (*NativeProcesses, capability.CredentialLookup, capability.DelegateSpec, ProcessServices) {
	t.Helper()
	authorities, _, lookup, spec, services := processAuthorityFixture(t)
	t.Cleanup(func() { _ = authorities.Close() })
	processes := &NativeProcesses{Authorities: authorities, Masks: services.Masks, URL: "http://host", Log: func(context.Context, string, string, nodeproto.FrameLog) error { return nil }}
	return processes, lookup, spec, *services
}

func TestNativeProcessMasksApplyOnlyToItsOwnOutput(t *testing.T) {
	processes, lookup, spec, services := nativeProcessesFixture(t)
	services.StartChild = func(context.Context, capability.Authorized, ChildRequest) (string, error) { return "child", nil }
	if err := processes.Masks.registerValue("accepted-secret"); err != nil {
		t.Fatal(err)
	}
	first, err := processes.Delegate(t.Context(), lookup, spec, services)
	if err != nil {
		t.Fatal(err)
	}
	one, err := processes.Authorities.processServices(t.Context(), first.Binding)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/node/v1/masks", strings.NewReader(`{"value":"other-job-log-text"}`))
	if err := one.write(request, capability.Authorized{}, "/node/v1/masks"); err != nil {
		t.Fatal(err)
	}
	resumed, err := processes.Delegate(t.Context(), lookup, spec, services)
	if err != nil {
		t.Fatal(err)
	}
	parent := processes.Authorities.deps.Parent.(*processParent)
	parent.snapshot.Authorities[0].Principal.Capability.Binding.NodeID = "other-job"
	parent.snapshot.Binding.Current.NodeID = "other-job"
	spec.Binding.NodeID, services.Job.ID = "other-job", "other-job"
	second, err := processes.Delegate(t.Context(), lookup, spec, services)
	if err != nil {
		t.Fatal(err)
	}
	if err := processes.Masks.registerValue("late-accepted-secret"); err != nil {
		t.Fatal(err)
	}
	if err := processes.Masks.registerIssued("late-issued-secret"); err != nil {
		t.Fatal(err)
	}
	if err := processes.Masks.acceptPlan(nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "generated-job"}}}, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest("POST", "/node/v1/masks", strings.NewReader(`{"value":"generated-job"}`))
	if err := one.write(request, capability.Authorized{}, "/node/v1/masks"); err == nil {
		t.Fatal("job mask ignored an identifier accepted after delegation")
	}
	var output []string
	processes.Log = func(_ context.Context, _, _ string, frame nodeproto.FrameLog) error {
		output = append(output, frame.Message)
		return nil
	}
	for _, grant := range []ProcessGrant{first, resumed, second} {
		config, err := processes.configuration(t.Context(), grant.Binding, grant.Revoke, ExecutorActivity{}, AcceptedRun{Spec: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		if err := config.Log(t.Context(), nodeproto.FrameLog{Message: "accepted-secret late-accepted-secret late-issued-secret other-job-log-text"}); err != nil {
			t.Fatal(err)
		}
		bound, err := processes.Authorities.processServices(t.Context(), grant.Binding)
		if err != nil {
			t.Fatal(err)
		}
		child := httptest.NewRequest("POST", "/node/v1/children", strings.NewReader(`{"pipeline":"build","args":{"value":"other-job-log-text"}}`))
		_, _, err = bound.read(child, capability.Authorized{}, "/node/v1/children")
		if (err != nil) != (grant.Binding.JobID == first.Binding.JobID) {
			t.Fatalf("child argument masking crossed its job scope: job=%s error=%v", grant.Binding.JobID, err)
		}
	}
	if len(output) != 3 || output[0] != "*** *** *** ***" || output[1] != "*** *** *** ***" || output[2] != "*** *** *** other-job-log-text" {
		t.Fatalf("process log masking crossed its scope: %q", output)
	}
}

func FuzzNativeProcessServicesIsolation(f *testing.F) {
	f.Add("run", "job", false)
	f.Add("foreign", "job", false)
	f.Add("run", "foreign", false)
	f.Add("run", "job", true)
	f.Fuzz(func(t *testing.T, run, job string, ended bool) {
		processes, lookup, spec, services := nativeProcessesFixture(t)
		grant, err := processes.Delegate(t.Context(), lookup, spec, services)
		if err != nil {
			t.Fatal(err)
		}
		if ended {
			if err := grant.Revoke(); err != nil {
				t.Fatal(err)
			}
		}
		binding := grant.Binding
		binding.RunID, binding.JobID = run, job
		bound, err := processes.Authorities.processServices(t.Context(), binding)
		valid := !ended && run == "run" && job == "job"
		if valid != (err == nil) || !valid && bound != nil {
			t.Fatal("service lookup crossed its delegated process binding")
		}
		if valid && (bound.Masks == processes.Masks || bound.Cleanups == nil || bound.Cleanups == services.Cleanups) {
			t.Fatal("delegation lost its isolated masks or cleanups")
		}
	})
}

func TestNativeProcessesKeepIndependentCleanupLedgers(t *testing.T) {
	processes, lookup, spec, services := nativeProcessesFixture(t)
	first, err := processes.Delegate(t.Context(), lookup, spec, services)
	if err != nil {
		t.Fatal(err)
	}
	second, err := processes.Delegate(t.Context(), lookup, spec, services)
	if err != nil {
		t.Fatal(err)
	}
	one, err := processes.Authorities.processServices(t.Context(), first.Binding)
	if err != nil {
		t.Fatal(err)
	}
	two, err := processes.Authorities.processServices(t.Context(), second.Binding)
	if err != nil || one.Cleanups == two.Cleanups {
		t.Fatal("separate processes shared cleanup registrations")
	}
	id, err := one.Cleanups.register([]string{"/unused"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := two.Cleanups.release(id); err == nil || !one.Cleanups.contains(id) {
		t.Fatal("another process released a cleanup")
	}
	refused := errors.New("log delivery refused")
	processes.Log = func(_ context.Context, run, job string, record nodeproto.FrameLog) error {
		if run != "run" || job != "job" || strings.Contains(record.Message, first.Binding.Routes.Bearer) {
			t.Error("cleanup log lost attribution or exposed process authority")
		}
		return refused
	}
	config, err := processes.configuration(t.Context(), first.Binding, first.Revoke, ExecutorActivity{}, AcceptedRun{Spec: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Revoke(); err != nil {
		t.Fatal(err)
	}
	if err := config.RunCleanups(t.Context(), CleanupScope{}); !errors.Is(err, refused) {
		t.Fatal("cleanup delivery failure was hidden:", err)
	}
	if one.Cleanups.contains(id) || two.Cleanups.closed {
		t.Fatal("cleanup changed another process's ledger")
	}
	if _, err := processes.Authorities.processServices(t.Context(), second.Binding); err != nil {
		t.Fatal("cleanup revoked another process:", err)
	}
}

func TestNativeProcessesRejectEndedServicesBeforeProjection(t *testing.T) {
	processes, lookup, spec, services := nativeProcessesFixture(t)
	grant, err := processes.Delegate(t.Context(), lookup, spec, services)
	if err != nil {
		t.Fatal(err)
	}
	if err := grant.Revoke(); err != nil {
		t.Fatal(err)
	}
	_, err = processes.Start(t.Context(), NodeInput{RouteBinding: grant.Binding, processRevoke: grant.Revoke}, ExecutorActivity{})
	var canonical *apierr.Error
	if !errors.As(err, &canonical) || canonical.Code != apierr.Unauthenticated {
		t.Fatal("ended services reached native startup:", err)
	}
}

func TestNativeProcessChildAwaitPausesItsAttemptIdleBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		processes, lookup, spec, services := nativeProcessesFixture(t)
		services.AwaitChild = func(ctx context.Context, _ capability.Authorized, child, _ string, _ time.Duration) (ProcessChildResult, error) {
			select {
			case <-time.After(2 * time.Second):
				return ProcessChildResult{RunID: child, Status: "running"}, nil
			case <-ctx.Done():
				return ProcessChildResult{}, context.Cause(ctx)
			}
		}
		grant, err := processes.Delegate(t.Context(), lookup, spec, services)
		if err != nil {
			t.Fatal(err)
		}
		ctx, activity, stop := executorProgressContext(t.Context(), time.Second)
		defer stop()
		if _, err := processes.configuration(ctx, grant.Binding, grant.Revoke, activity, AcceptedRun{Spec: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
		bound, err := processes.Authorities.processServices(ctx, grant.Binding)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest("GET", "/node/v1/children/child/await?wait_ms=3000", nil).WithContext(ctx)
		request.SetPathValue("run", "child")
		if _, err := bound.awaitChild(request, capability.Authorized{}); err != nil || ctx.Err() != nil {
			t.Fatal("child wait consumed the idle budget:", err)
		}
		<-ctx.Done()
		if !errors.Is(context.Cause(ctx), errExecutorNoProgress) {
			t.Fatal("child wait did not resume the idle budget")
		}
	})
}
