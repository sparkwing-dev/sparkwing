package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// A runner places a run's Jobs by the team a claim and a read return, so
// both must report the trigger's own team rather than an empty one.
func TestTrigger_ClaimAndReadReturnTheOwningTeam(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	acme := teamHandle(t, s, "acme")
	if err := acme.CreateTrigger(ctx, store.Trigger{ID: "t-acme", Pipeline: "demo", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}
	// Control: the unauthenticated local claim is the default team's, so it
	// leaves acme's trigger alone.
	if tr, err := s.ClaimNextTriggerFor(ctx, store.ClaimIdentity{}, time.Minute, nil, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("default-team claim = %+v, %v; want nothing", tr, err)
	}

	_, tok, err := acme.CreateToken(ctx, "acme-runner", store.TokenKindRunner, []string{"triggers.claim"}, time.Hour, time.Now())
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	claimed, err := s.ClaimNextTriggerFor(ctx,
		store.ClaimIdentity{Principal: "acme-runner", TokenPrefix: tok.Prefix}, time.Minute, nil, nil)
	if err != nil {
		t.Fatalf("ClaimNextTriggerFor: %v", err)
	}
	if claimed.ID != "t-acme" || claimed.Team != "acme" {
		t.Fatalf("claimed %s team = %q, want t-acme in acme", claimed.ID, claimed.Team)
	}
	read, err := s.GetTrigger(ctx, "t-acme")
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}
	if read.Team != "acme" {
		t.Fatalf("read team = %q, want acme", read.Team)
	}

	seedPending(t, s, "t-legacy")
	legacy, err := s.GetTrigger(ctx, "t-legacy")
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}
	if legacy.Team != store.DefaultTeam {
		t.Fatalf("unscoped trigger team = %q, want %q", legacy.Team, store.DefaultTeam)
	}
}

// A trigger's idempotency key, webhook delivery id and replay digest are
// unique within its team, so one team holding a value neither refuses another
// team the same value nor tells it the value is taken. A repeat inside one
// team is still refused.
func TestCreateTriggerKeysReplayAndIdempotencyPerTeam(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	if err := st.AsOperator().CreateTeam(ctx, "team-b"); err != nil {
		t.Fatal(err)
	}
	operator, err := st.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	tenantB, err := st.ForTeam(ctx, "team-b")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		set  func(*store.Trigger)
		want error
	}{
		{"idempotency key", func(tr *store.Trigger) { tr.IdempotencyKey = "k-1" }, store.ErrDuplicateIdempotencyKey},
		{"webhook delivery", func(tr *store.Trigger) { tr.WebhookDelivery = "d-1" }, store.ErrDuplicateWebhookDelivery},
		{"replay key", func(tr *store.Trigger) { tr.WebhookReplayKey = "r-1" }, store.ErrDuplicateWebhookDelivery},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trig := func(id string) store.Trigger {
				tr := store.Trigger{ID: id, Pipeline: "build", CreatedAt: time.Now()}
				tc.set(&tr)
				return tr
			}
			prefix := strings.ReplaceAll(tc.name, " ", "-")
			if err := operator.CreateTrigger(ctx, trig(prefix+"-a")); err != nil {
				t.Fatal(err)
			}
			if err := tenantB.CreateTrigger(ctx, trig(prefix+"-b")); err != nil {
				t.Fatalf("team B reusing the operator's %s = %v, want accepted", tc.name, err)
			}
			if err := tenantB.CreateTrigger(ctx, trig(prefix+"-c")); !errors.Is(err, tc.want) {
				t.Fatalf("team B repeating its own %s = %v, want %v", tc.name, err, tc.want)
			}
		})
	}
}
