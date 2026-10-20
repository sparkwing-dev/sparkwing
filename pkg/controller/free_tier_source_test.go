package controller

import (
	"bytes"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func freeTierStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func TestSignUpFreeTierClosesWithTheLastSlot(t *testing.T) {
	s := freeTierStore(t)
	if err := s.SetFreeTeamSlots(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	server := New(s, nil)
	if got, err := server.SignUpFreeTier(t.Context()); err != nil || got != FreeTierOpen {
		t.Fatalf("with a free slot: %s, %v", got, err)
	}
	if err := s.AsOperator().CreateTeam(t.Context(), "first"); err != nil {
		t.Fatal(err)
	}
	if err := s.GrantFreeSlot(t.Context(), "first", time.Now()); err != nil {
		t.Fatal(err)
	}
	if got, err := server.SignUpFreeTier(t.Context()); err != nil || got != FreeTierClosed {
		t.Fatalf("with every slot taken: %s, %v", got, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := server.SignUpFreeTier(t.Context()); err == nil || got != FreeTierUnreadable {
		t.Fatalf("with an unreadable store: %s, %v", got, err)
	}
}

func TestTheServerIsItsOwnFreeTierSource(t *testing.T) {
	s := freeTierStore(t)
	if err := s.SetFreeTeamSlots(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	server := NewDeployment(s, deploymenttest.Team(t), slog.New(slog.NewJSONHandler(&logs, nil)))
	server.WithFreeTier(server).CheckFreeTierSource()
	if strings.Contains(logs.String(), "signup.free_tier_unwired") {
		t.Fatalf("configured source logged as missing: %s", logs.String())
	}
	if conditions := server.signUpConditions(t.Context()); !conditions.FreeTierClosed || conditions.FreeTierUnreadable {
		t.Fatalf("full tier gate: %+v", conditions)
	}
}
