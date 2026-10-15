package localcontroller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

func TestNativeServicesRefusesUnknownAuthorityBeforeOpeningResources(t *testing.T) {
	var binding Binding
	c := openFixture(t, func(_ context.Context, b Binding) (Services, error) {
		binding = b
		return serviceFixture(b), nil
	})
	home := filepath.Join(t.TempDir(), "unopened")
	principal := capability.Principal{Class: capability.CredentialBearer, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "forged"},
		Capability: capability.Capability{Team: string(binding.Tenant.Team()), Binding: capability.Binding{RunID: "unavailable"}}}
	refreshed := false
	services, err := NativeServices(t.Context(), home, c.store, runners.StoreBinding{Tenant: binding.Tenant, Principal: principal, Refresh: func(ctx context.Context) (capability.Principal, error) {
		refreshed = true
		route, _ := capability.LookupRoute("POST", "/api/v1/runs/{run}/heartbeat")
		_, err := capability.NewAuthenticator(c.store).Authenticate(ctx, capability.Presentation{Class: capability.CredentialBearer, Raw: capability.ControllerPrefix + base64.RawURLEncoding.EncodeToString(make([]byte, 32))}, route)
		return capability.Principal{}, err
	}}, source.Options{})
	if err == nil || services.Config.Store != nil || services.Close != nil {
		t.Fatalf("unknown authority produced native services: %+v, %v", services, err)
	}
	if _, err := os.Stat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unauthenticated setup created private resources: %v", err)
	}
	if !refreshed {
		t.Fatal("native construction did not reauthorize its award")
	}
}

type nativeAuthentication struct {
	snapshot capability.AuthenticationSnapshot
}

func (a *nativeAuthentication) ReadAuthentication(context.Context, capability.CredentialLookup, capability.RequestEvidence) (capability.AuthenticationSnapshot, error) {
	return a.snapshot, nil
}

func (*nativeAuthentication) CheckBinding(context.Context, capability.Principal, capability.BindingRule, capability.Target) error {
	return nil
}

func FuzzLocalNativeLogsRejectLostClaimFence(f *testing.F) {
	f.Add(uint8(0), uint32(1))
	f.Add(uint8(1), uint32(2))
	f.Add(uint8(2), uint32(3))
	f.Add(uint8(3), uint32(4))
	f.Add(uint8(4), uint32(5))
	f.Add(uint8(5), uint32(6))
	f.Fuzz(func(t *testing.T, change uint8, amount uint32) {
		now := time.Unix(10_000, 0)
		raw := capability.ControllerPrefix + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
		principal := capability.Principal{Class: capability.CredentialBearer, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "claim"}, Capability: capability.Capability{
			Prefix: "claim", Digest: sha256.Sum256([]byte(raw)), Audience: capability.Controller,
			Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "runner"}, Team: "default", Unit: capability.UnitNode, UnitID: "claim", Delivery: capability.DeliveryDirect,
			Issuance: capability.Award, Purpose: capability.PurposeWork, Grants: []capability.Grant{capability.NodeReport},
			IssuedAt: now.Add(-time.Hour), Binding: capability.Binding{Kind: capability.BindingNode, RunID: "run", NodeID: "job", Holder: "holder", Generation: 1, Attempt: 1},
		}}
		state := &nativeAuthentication{snapshot: capability.AuthenticationSnapshot{Now: now,
			Authorities: []capability.AuthorityState{{Principal: principal, MembershipValid: true}},
			Binding:     capability.BindingState{Current: principal.Capability.Binding, LeaseExpiresAt: now.Add(time.Minute)},
		}}
		route, ok := capability.LookupRoute("POST", "/api/v1/runs/{run}/jobs/{job}/heartbeat")
		if !ok {
			t.Fatal("common renewal policy missing")
		}
		refresh := func(ctx context.Context) (capability.Principal, error) {
			auth, err := capability.NewAuthenticator(state).Authenticate(ctx, capability.Presentation{Class: capability.CredentialBearer, Raw: raw}, route)
			if err != nil {
				return capability.Principal{}, err
			}
			authorized, err := capability.Authorize(ctx, state, route, auth, capability.Target{Team: "default", RunID: "run", NodeID: "job"})
			return authorized.Principal, err
		}
		home := t.TempDir()
		log := localHostLog(home, "run", refresh, secrets.NewMasker())
		if err := log(t.Context(), "run", "job", nodeproto.FrameLog{Message: "before holder loss"}); err != nil {
			t.Fatal(err)
		}
		files, err := os.ReadDir(filepath.Join(home, "run-logs"))
		if err != nil || len(files) != 1 {
			t.Fatalf("initial log: %v", err)
		}
		file := filepath.Join(home, "run-logs", files[0].Name())
		before, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		switch change % 6 {
		case 0:
			state.snapshot.Binding.Current.Generation += int64(amount) + 1
		case 1:
			state.snapshot.Binding.Current.Attempt += int64(amount) + 1
		case 2:
			state.snapshot.Binding.Current.Holder += "-replacement"
		case 3:
			state.snapshot.Binding.LeaseExpiresAt = now
		case 4:
			state.snapshot.Binding.FinishedAt = &now
		case 5:
			state.snapshot.Binding.RunFinished = true
		}
		if err := log(t.Context(), "run", "job", nodeproto.FrameLog{Message: "after holder loss"}); err == nil {
			t.Fatal("stale claim wrote a log")
		}
		after, err := os.ReadFile(file)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("stale claim changed the log: %v", err)
		}
	})
}

func TestLocalStateSubscriptionJoinsAfterCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		subscription, err := localStateSubscription(ctx, "run")
		if err != nil {
			t.Fatal(err)
		}
		<-subscription.Changed
		cancel()
		if err := subscription.Close(); err != nil {
			t.Fatal(err)
		}
		if err := subscription.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

func FuzzLocalNativeLogsPreserveAuthorityAndMasking(f *testing.F) {
	f.Add("secret-long-value", "job", false, false)
	f.Add("secret-long-value", "../../escape", true, false)
	f.Add("secret-long-value", "job", false, true)
	f.Fuzz(func(t *testing.T, value, job string, foreign, revoked bool) {
		digest := sha256.Sum256([]byte(value))
		secret := hex.EncodeToString(digest[:])
		masker := secrets.NewMasker()
		masker.Register(secret)
		home := t.TempDir()
		refused := errors.New("revoked")
		log := localHostLog(home, "run", func(context.Context) (capability.Principal, error) {
			if revoked {
				return capability.Principal{}, refused
			}
			return capability.Principal{}, nil
		}, masker)
		run := "run"
		if foreign {
			run = "foreign"
		}
		attrs, err := json.Marshal(map[string]string{secret: secret})
		if err != nil {
			t.Fatal(err)
		}
		err = log(t.Context(), run, job, nodeproto.FrameLog{Message: secret, Attrs: attrs})
		files, readErr := os.ReadDir(filepath.Join(home, "run-logs"))
		if foreign || revoked {
			if err == nil || !errors.Is(readErr, os.ErrNotExist) {
				t.Fatalf("refused authority wrote logs: %v, %v", err, readErr)
			}
			return
		}
		if err != nil || readErr != nil || len(files) != 1 {
			t.Fatalf("authorized log missing: %v, %v", err, readErr)
		}
		data, err := os.ReadFile(filepath.Join(home, "run-logs", files[0].Name()))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(secret)) || !json.Valid(bytes.TrimSpace(data)) {
			t.Fatalf("unsafe log: %s", data)
		}
	})
}
