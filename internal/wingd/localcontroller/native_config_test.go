package localcontroller

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type nativeState struct {
	nodehost.NativeHostStore
	snapshot store.RunSnapshot
}

func (s *nativeState) ReadSnapshot(ctx context.Context, _ string) (store.RunSnapshot, error) {
	return s.snapshot, context.Cause(ctx)
}

type nativeEngine struct{}

func (nativeEngine) Resolve(context.Context, engine.Pin, releaseasset.Target) (engine.Resolved, error) {
	return engine.Resolved{}, errors.New("source boundary")
}

func (nativeEngine) Ensure(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
	return engine.Resolved{}, errors.New("engine boundary")
}

func nativeConfigFixture(t testing.TB) (NativeConfig, *nativeState) {
	t.Helper()
	_, authority, _, _ := localClaimsFixture(t)
	principal := authority.snapshot.Authorities[0].Principal
	home := t.TempDir()
	snapshots, err := nodehost.NewFileEnvironmentSnapshots(filepath.Join(home, "environments"))
	if err != nil {
		t.Fatal(err)
	}
	seal, err := snapshots.Capture(t.Context(), []string{"RETAINED=registration"})
	if err != nil {
		t.Fatal(err)
	}
	state := &nativeState{snapshot: store.RunSnapshot{Run: store.RunRow{
		ID: "run", Team: store.DefaultTeam,
		Request: runspec.SealedRequest{Request: runspec.Request{Team: string(store.DefaultTeam), Env: seal}},
	}}}
	return NativeConfig{
		Home: home, Version: "v0.71.0", Store: state, Parent: authority, Bindings: authority,
		Principal: principal, Refresh: func(context.Context) (capability.Principal, error) { return principal, nil },
		Renew:  func(context.Context, string, bool) (store.LeaseBeat, error) { return store.LeaseBeat{}, nil },
		Source: source.Options{Engines: nativeEngine{}},
	}, state
}

func TestNativeServicesUsesSharedInterfacesAndCapturedEnvironment(t *testing.T) {
	cfg, state := nativeConfigFixture(t)
	listener := &processListener{closed: make(chan struct{})}
	services, err := nativeServices(t.Context(), cfg, func(string, string) (net.Listener, error) { return listener, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := services.Close(); err != nil {
			t.Error(err)
		}
	})
	if services.Config.Store != state || !services.Config.Source.Base.Local || !reflect.DeepEqual(services.Config.Source.Base.Captured, []string{"RETAINED=registration"}) {
		t.Fatal("native composition replaced state or captured inputs")
	}
	owner, claimed, evaluator, err := nodehost.NewNativeHost(services.Config)
	if err != nil || owner == nil || claimed == nil || evaluator == nil {
		t.Fatalf("shared native hosts cannot use the supplied interfaces: %v", err)
	}
}

func FuzzNativeBindingsRejectForeignScopeBeforeTransport(f *testing.F) {
	f.Add(uint8(1), "foreign", false)
	f.Add(uint8(2), "foreign", true)
	f.Add(uint8(4), "foreign", false)
	f.Fuzz(func(t *testing.T, fields uint8, suffix string, refreshed bool) {
		cfg, state := nativeConfigFixture(t)
		fields = fields%7 + 1
		if refreshed {
			principal := cfg.Principal
			if fields&1 != 0 {
				principal.Authority.ID += "-foreign-" + suffix
			}
			if fields&2 != 0 {
				principal.Capability.Team += "-foreign-" + suffix
			}
			if fields&4 != 0 {
				principal.Capability.Binding.RunID += "-foreign-" + suffix
			}
			cfg.Refresh = func(context.Context) (capability.Principal, error) { return principal, nil }
		} else {
			if fields&1 != 0 {
				state.snapshot.Run.ID += "-foreign-" + suffix
			}
			if fields&2 != 0 {
				state.snapshot.Run.Team += store.Team("-foreign-" + suffix)
			}
			if fields&4 != 0 {
				state.snapshot.Run.Request.Request.Team += "-foreign-" + suffix
			}
		}
		opened := false
		services, err := nativeServices(t.Context(), cfg, func(string, string) (net.Listener, error) {
			opened = true
			return nil, errors.New("unexpected transport")
		})
		if err == nil || opened || services.Close != nil {
			t.Fatal("foreign state reached process service construction")
		}
	})
}
