package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func billingCLIConnect(t *testing.T, role string, grants ...capability.Grant) (func(context.Context, string) (controllerwire.StandingManager, func(), error), *store.Store) {
	t.Helper()
	state, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := state.Close(); err != nil {
			t.Error(err)
		}
	})
	teststore.SeedTeam(t, state, "billing-test")
	for _, query := range []string{
		`INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('member','member@example.test',1,1)`,
		`INSERT INTO memberships(team,account_id,role,created_at) VALUES ('billing-test','member','` + role + `',1)`,
	} {
		if _, err := teststore.SQL(t, state).ExecContext(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	server := controller.NewDeployment(state, deploymenttest.Team(t), nil).WithSecretsCipher(cipher)
	session, _, _, err := state.CreateAccountSession(t.Context(), store.Account{ID: "member"}, "billing-test", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(session))
	authority := capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}
	member, err := state.ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMember, Authority: &authority, Team: "billing-test", Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "member"}, Grants: grants, Binding: capability.Binding{Kind: capability.BindingNone}})
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	return func(_ context.Context, profile string) (controllerwire.StandingManager, func(), error) {
		if profile != "work" {
			t.Fatalf("profile=%q", profile)
		}
		manager, err := client.NewStandingManager(client.EngineOptions{URL: "https://controller.test", Bearer: func(context.Context) (string, error) { return member.Raw, nil }, HTTP: &http.Client{Transport: logRoundTrip(func(r *http.Request) (*http.Response, error) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			return w.Result(), nil
		})}})
		return manager, func() {}, err
	}, state
}

func runBillingArgs(ctx context.Context, args []string, out *bytes.Buffer, connect func(context.Context, string) (controllerwire.StandingManager, func(), error)) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"billing"}, args...))
	if err != nil {
		return err
	}
	return runBillingCommand(ctx, inv, out, connect)
}

func TestBillingCLIRequestsHighStandingThroughControllerAndStore(t *testing.T) {
	connect, state := billingCLIConnect(t, "owner", capability.RunsRead, capability.TeamAdmin)
	var out bytes.Buffer
	if err := runBillingArgs(t.Context(), []string{"standing", "--profile", "work", "-o", "pretty"}, &out, connect); err != nil || out.String() != "Standing: New\nHistory tier: New\nPrepaid funds can reach: Established\n" {
		t.Fatalf("standing: %q %v", out.String(), err)
	}
	out.Reset()
	if err := runBillingArgs(t.Context(), []string{"request-tier", "--tier", "high", "--message", "Nightly fuzzing needs 256 vCPU.", "--profile", "work", "-o", "json"}, &out, connect); err != nil {
		t.Fatal(err)
	}
	var made controllerwire.StandingRequest
	if err := json.Unmarshal(out.Bytes(), &made); err != nil || made.State != "open" || made.Tier != "High" || made.Message != "Nightly fuzzing needs 256 vCPU." {
		t.Fatalf("request: %s %v", out.String(), err)
	}
	out.Reset()
	err := runBillingArgs(t.Context(), []string{"request-tier", "--tier", "high", "--message", "again", "--profile", "work"}, &out, connect)
	var refusal *apierr.Error
	if !errors.As(err, &refusal) || refusal.Code != apierr.Conflict || out.Len() != 0 {
		t.Fatalf("second request: %q %v", out.String(), err)
	}
	open, err := state.OpenStandingRequests(t.Context(), 10)
	if err != nil || len(open) != 1 || open[0].ID != made.ID || open[0].RequestedBy != "member" {
		t.Fatalf("operator queue: %+v %v", open, err)
	}
	out.Reset()
	if err := runBillingArgs(t.Context(), []string{"standing", "--profile", "work", "-o", "json"}, &out, connect); err != nil {
		t.Fatal(err)
	}
	var standing controllerwire.TeamStanding
	if err := json.Unmarshal(out.Bytes(), &standing); err != nil || standing.Request == nil || standing.Request.ID != made.ID || standing.CanRequest {
		t.Fatalf("standing after request: %s %v", out.String(), err)
	}
}

func TestBillingCLIRefusesMembersAndInvalidTiers(t *testing.T) {
	connect, state := billingCLIConnect(t, "editor", capability.RunsRead, capability.RunsWrite)
	var out bytes.Buffer
	err := runBillingArgs(t.Context(), []string{"request-tier", "--tier", "high", "--message", "m", "--profile", "work"}, &out, connect)
	var refusal *apierr.Error
	if !errors.As(err, &refusal) || refusal.Code != apierr.Forbidden || out.Len() != 0 {
		t.Fatalf("editor request: %q %v", out.String(), err)
	}
	for _, args := range [][]string{
		{"request-tier", "--tier", "ultra", "--message", "m", "--profile", "work"},
		{"request-tier", "--tier", "high", "--profile", "work"},
		{"request-tier", "--message", "m", "--profile", "work"},
	} {
		if _, err := clisurface.Parse("sparkwing", append([]string{"billing"}, args...)); err == nil {
			t.Fatalf("parsed %v", args)
		}
	}
	if open, err := state.OpenStandingRequests(t.Context(), 10); err != nil || len(open) != 0 {
		t.Fatalf("refused requests reached the queue: %+v %v", open, err)
	}
	if err := runBillingArgs(t.Context(), []string{"standing", "--profile", "work", "-o", "plain"}, &out, connect); err != nil || strings.TrimSpace(out.String()) != "New" {
		t.Fatalf("member standing read: %q %v", out.String(), err)
	}
}
