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

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

type overflowManager struct {
	control controllerwire.OverflowControl
	err     error
	reads   int
	writes  int
}

func TestOverflowCLIThroughAuthenticatedControllerAndStore(t *testing.T) {
	state, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := state.Close(); err != nil {
			t.Error(err)
		}
	})
	teststore.SeedTeam(t, state, "overflow-test")
	for _, query := range []string{
		`INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('member','member@example.test',1,1)`,
		`INSERT INTO memberships(team,account_id,role,created_at) VALUES ('overflow-test','member','editor',1)`,
	} {
		if _, err := teststore.SQL(t, state).ExecContext(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	server := controller.New(state, nil).WithSecretsCipher(cipher)
	session, _, _, err := state.CreateAccountSession(t.Context(), store.Account{ID: "member"}, "overflow-test", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(session))
	authority := capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}
	member, err := state.ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMember, Authority: &authority, Team: "overflow-test", Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "member"}, Grants: []capability.Grant{capability.RunsRead, capability.RunsWrite}, Binding: capability.Binding{Kind: capability.BindingNone}})
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	closed := 0
	connect := func(_ context.Context, profile string) (controllerwire.OverflowManager, func(), error) {
		if profile != "work" {
			t.Fatalf("profile=%q", profile)
		}
		manager, err := client.NewOverflowManager(client.EngineOptions{URL: "https://controller.test", Bearer: func(context.Context) (string, error) { return member.Raw, nil }, HTTP: &http.Client{Transport: logRoundTrip(func(r *http.Request) (*http.Response, error) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			return w.Result(), nil
		})}})
		return manager, func() { closed++ }, err
	}
	for _, tc := range []struct {
		args  []string
		plain string
	}{
		{args: []string{"show"}, plain: "uncapped\n"},
		{args: []string{"set", "--minutes", "12"}, plain: "12\n"},
		{args: []string{"show"}, plain: "12\n"},
		{args: []string{"set", "--minutes", "0"}, plain: "0\n"},
		{args: []string{"set", "--uncapped"}, plain: "uncapped\n"},
		{args: []string{"show"}, plain: "uncapped\n"},
	} {
		var out bytes.Buffer
		if err := runOverflowCommandArgs(t.Context(), append(tc.args, "--profile", "work", "-o", "plain"), &out, connect); err != nil || out.String() != tc.plain {
			t.Fatalf("overflow %v: %q %v", tc.args, out.String(), err)
		}
	}
	if closed != 6 {
		t.Fatalf("closed %d transports, want 6", closed)
	}
}

func TestOverflowSetCanCapDisableAndUncapCloud(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		limit *int64
	}{
		{args: []string{"--minutes", "120"}, limit: new(int64(120))},
		{args: []string{"--minutes", "0"}, limit: new(int64)},
		{args: []string{"--uncapped"}},
	} {
		args := append([]string{"overflow", "set", "--profile", "work", "-o", "json"}, tc.args...)
		inv, err := clisurface.Parse("sparkwing", args)
		if err != nil {
			t.Fatal(err)
		}
		manager := &overflowManager{}
		var out bytes.Buffer
		if err := manageOverflow(t.Context(), inv, manager, &out); err != nil {
			t.Fatal(err)
		}
		if manager.writes != 1 || manager.reads != 0 || (manager.control.MonthlyMinutes == nil) != (tc.limit == nil) || tc.limit != nil && *manager.control.MonthlyMinutes != *tc.limit {
			t.Fatalf("set: %+v", manager)
		}
		var got controllerwire.OverflowControl
		if json.Unmarshal(out.Bytes(), &got) != nil || !strings.Contains(out.String(), `"monthly_minutes":`) {
			t.Fatalf("set receipt: %s", out.String())
		}
	}
}

func TestOverflowInvalidRequestsAndRefusalsWriteNoReceipt(t *testing.T) {
	for _, args := range [][]string{
		{"show"},
		{"set", "--profile", "work"},
		{"set", "--profile", "work", "--minutes", "2", "--uncapped"},
		{"set", "--profile", "work", "--minutes", "-1"},
		{"set", "--profile", "work", "--minutes", "153722867280912931"},
	} {
		inv, err := clisurface.Parse("sparkwing", append([]string{"overflow"}, args...))
		if err == nil {
			manager := &overflowManager{}
			var out bytes.Buffer
			err = manageOverflow(t.Context(), inv, manager, &out)
			if manager.reads != 0 || manager.writes != 0 || out.Len() != 0 {
				t.Fatal("invalid request reached the controller")
			}
		}
		if err == nil {
			t.Fatalf("accepted invalid overflow request: %v", args)
		}
	}
	denied := errors.New("account authority refused")
	for _, args := range [][]string{{"show"}, {"set", "--uncapped"}} {
		inv, err := clisurface.Parse("sparkwing", append(append([]string{"overflow"}, args...), "--profile", "work"))
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := manageOverflow(t.Context(), inv, &overflowManager{err: denied}, &out); !errors.Is(err, denied) || out.Len() != 0 {
			t.Fatalf("refusal: %s %v", out.String(), err)
		}
	}
}

func (m *overflowManager) ReadOverflowControl(context.Context) (controllerwire.OverflowControl, error) {
	m.reads++
	return m.control, m.err
}

func (m *overflowManager) SetOverflowLimit(_ context.Context, minutes *int64) (controllerwire.OverflowControl, error) {
	m.writes++
	m.control.MonthlyMinutes = minutes
	return m.control, m.err
}

func TestOverflowShowReportsAllowanceAndUsage(t *testing.T) {
	for _, format := range []string{"json", "plain", "pretty"} {
		t.Run(format, func(t *testing.T) {
			inv, err := clisurface.Parse("sparkwing", []string{"overflow", "show", "--profile", "work", "-o", format})
			if err != nil {
				t.Fatal(err)
			}
			manager := &overflowManager{control: controllerwire.OverflowControl{UsedSeconds: 75, PeriodStart: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}}
			var out bytes.Buffer
			if err := manageOverflow(t.Context(), inv, manager, &out); err != nil {
				t.Fatal(err)
			}
			if manager.reads != 1 || manager.writes != 0 {
				t.Fatal("show changed the user's budget")
			}
			if format == "json" {
				var got controllerwire.OverflowControl
				if json.Unmarshal(out.Bytes(), &got) != nil || got.MonthlyMinutes != nil || got.UsedSeconds != 75 || got.PeriodStart != manager.control.PeriodStart {
					t.Fatalf("show: %s", out.String())
				}
			} else if format == "plain" && out.String() != "uncapped\n" {
				t.Fatalf("plain limit: %q", out.String())
			} else if format == "pretty" && out.String() != "Monthly limit: uncapped\nUsed: 75 seconds\nPeriod start: 2026-10-01T00:00:00Z\n" {
				t.Fatalf("show: %q", out.String())
			}
		})
	}
}
