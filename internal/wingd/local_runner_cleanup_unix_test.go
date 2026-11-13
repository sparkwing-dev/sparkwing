//go:build unix

package wingd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type cleanupRunRegistry struct {
	FuncRunStore
	LocalRunnerStore
}

type cleanupRegistryAdapter struct{ *store.Operator }

func cleanupWireInstance(i store.LocalRunnerInstance) localhostwire.LocalRunnerInstance {
	return localhostwire.LocalRunnerInstance{ID: i.ID, CapabilityPrefix: i.CapabilityPrefix, Machine: i.Machine, PID: i.PID, ProcessBirth: i.ProcessBirth, Version: i.Version, BuildIdentity: i.BuildIdentity, State: i.State}
}

func (r cleanupRegistryAdapter) ActiveLocalRunner(ctx context.Context, subject string) (localhostwire.LocalRunnerInstance, error) {
	i, err := r.Operator.ActiveLocalRunner(ctx, subject)
	return cleanupWireInstance(i), err
}

func (r cleanupRegistryAdapter) ReadLocalRunner(ctx context.Context, subject, id string) (localhostwire.LocalRunnerInstance, error) {
	i, err := r.Operator.ReadLocalRunner(ctx, subject, id)
	return cleanupWireInstance(i), err
}

func (r cleanupRegistryAdapter) RegisterLocalRunner(ctx context.Context, subject string, i localhostwire.LocalRunnerInstance) (localhostwire.LocalRunnerInstance, error) {
	result, err := r.Operator.RegisterLocalRunner(ctx, subject, store.LocalRunnerInstance{ID: i.ID, CapabilityPrefix: i.CapabilityPrefix, PID: i.PID, ProcessBirth: i.ProcessBirth, Version: i.Version, BuildIdentity: i.BuildIdentity})
	return cleanupWireInstance(result), err
}

func TestControllerStopRetryAfterRunnerCredentialCleanup(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		name := "credential-live"
		if revoke {
			name = "credential-revoked"
		}
		t.Run(name, func(t *testing.T) {
			s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := s.Close(); err != nil {
					t.Error(err)
				}
			}()
			subject, err := CurrentLocalSubject()
			if err != nil {
				t.Fatal(err)
			}
			op := s.AsOperator()
			auth, err := op.ReadAuthentication(t.Context(), capability.CredentialLookup{Class: capability.CredentialPeer, Peer: capability.Subject{Kind: capability.SubjectLocal, ID: subject}}, capability.RequestEvidence{})
			if err != nil {
				t.Fatal(err)
			}
			root := auth.Authorities[0].Principal.Authority
			child, err := op.DelegateCapability(t.Context(), capability.DelegateSpec{Parent: root, Audience: capability.Controller, Grants: []capability.Grant{capability.NodesClaim, capability.TriggersClaim}, Binding: capability.Binding{Kind: capability.BindingNone}})
			if err != nil {
				t.Fatal(err)
			}
			process := exec.Command("/bin/sleep", "60")
			if err := process.Start(); err != nil {
				t.Fatal(err)
			}
			joined := false
			defer func() {
				if !joined {
					_ = process.Process.Kill()
					_ = process.Wait()
				}
			}()
			birth, err := admissionProcessBirth(process.Process.Pid)
			if err != nil {
				t.Fatal(err)
			}
			instance, err := op.RegisterLocalRunner(t.Context(), subject, store.LocalRunnerInstance{ID: "cleanup-runner", CapabilityPrefix: child.Capability.Prefix, PID: process.Process.Pid, ProcessBirth: birth, Version: "test", BuildIdentity: "test"})
			if err != nil {
				t.Fatal(err)
			}
			if err := op.ActivateLocalRunner(t.Context(), subject, instance.ID); err != nil {
				t.Fatal(err)
			}
			firstRequest := controllerHTTPRequest(t, http.MethodPost, "drain?runner=retire", "", true)
			firstCtx, cancel := context.WithCancel(firstRequest.Context())
			defer cancel()
			registry := &cleanupRunRegistry{LocalRunnerStore: cleanupRegistryAdapter{op}}
			d := newHTTPAdmissionTest(t, &admissionMemoryStore{})
			d.cfg.Runs = registry
			handler := d.controllerHTTP(http.NotFoundHandler())
			first := httptest.NewRecorder()
			started := time.Now()
			handler.ServeHTTP(first, firstRequest.WithContext(firstCtx))
			if first.Code != http.StatusInternalServerError {
				t.Fatalf("first stop=%d %s", first.Code, first.Body)
			}
			if !strings.Contains(first.Body.String(), "deadline exceeded") {
				t.Fatalf("first stop did not reach retirement timeout: %s", first.Body)
			}
			t.Logf("retirement timeout returned in %s: %s", time.Since(started), first.Body)
			state, err := op.ReadLocalRunner(t.Context(), subject, instance.ID)
			if err != nil || state.State != "draining" {
				t.Fatalf("drain was not persisted: %+v %v", state, err)
			}
			select {
			case <-d.quit:
				t.Fatal("first stop closed controller")
			default:
			}
			// safety: normal cleanup ends credential authority before this process disappears.
			if err := op.StopLocalRunner(t.Context(), subject, instance.ID); err != nil {
				t.Fatal(err)
			}
			if revoke {
				tenant, err := s.ForTeam(t.Context(), store.DefaultTeam)
				if err != nil {
					t.Fatal(err)
				}
				ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: instance.CapabilityPrefix}
				if err := tenant.RevokeAuthority(t.Context(), runstate.Actor{Authority: root}, ref); err != nil {
					t.Fatal(err)
				}
			}
			if revoke {
				live := httptest.NewRecorder()
				handler.ServeHTTP(live, controllerHTTPRequest(t, http.MethodPost, "drain?runner=retire", "", true))
				if live.Code != http.StatusInternalServerError || !strings.Contains(live.Body.String(), "authority has ended") {
					t.Fatalf("live revoked runner was not refused: %d %s", live.Code, live.Body)
				}
				select {
				case <-d.quit:
					t.Fatal("live revoked runner lost its controller")
				default:
				}
			}
			if err := process.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = process.Wait()
			joined = true
			alive, err := LocalRunnerProcessAlive(cleanupWireInstance(instance))
			if err != nil || alive {
				t.Fatalf("fixture process remains: alive=%v err=%v", alive, err)
			}
			t.Logf("first stop persisted draining, runner cleanup complete, PID %d absent, revoke=%v", instance.PID, revoke)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, controllerHTTPRequest(t, http.MethodPost, "drain?runner=retire", "", true))
			t.Logf("stop after joined exit: status=%d body=%s", response.Code, response.Body)
			if response.Code != http.StatusOK {
				t.Errorf("stop retry failed after runner exited: %d %s", response.Code, response.Body)
			}

			select {
			case <-d.quit:
			default:
				t.Error("controller remained open after confirmed runner exit")
			}
		})
	}
}
