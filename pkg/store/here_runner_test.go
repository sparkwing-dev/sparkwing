package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// safety: a pinned trigger is claimable by its own credential only, and only
// for its first claim; the queue claim never sees it.
func TestCreateHereTriggerWithRun_PinsTheTriggerToItsCredentialOnce(t *testing.T) {
	st := storetest.Open(t)
	ctx := store.WithoutCreditMetering(context.Background())
	tenant, err := st.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	raw, tok, err := tenant.CreateHereTriggerWithRun(ctx,
		store.Trigger{ID: "run-here", Pipeline: "hotfix", TriggerSource: "cli", CreatedAt: now},
		store.Run{ID: "run-here", Pipeline: "hotfix", Status: "pending", CreatedAt: now, StartedAt: now},
		store.HereRunner{Reason: "INC-42", By: "dev@example.com", Scopes: store.MachineTokenScopes(), TTL: time.Hour}, now)
	if err != nil {
		t.Fatal(err)
	}
	if raw[:store.PrefixLen] != tok.Prefix || tok.Principal != store.HereRunnerPrincipalPrefix+"run-here" {
		t.Fatalf("credential = %+v", tok)
	}
	_, fleetTok, err := st.CreateToken("fleet", store.TokenKindRunner, store.MachineTokenScopes(), 0, now)
	if err != nil {
		t.Fatal(err)
	}
	fleet := store.ClaimIdentity{Principal: "fleet", TokenPrefix: fleetTok.Prefix}
	here := store.ClaimIdentity{Principal: tok.Principal, TokenPrefix: tok.Prefix}
	if _, err := st.ClaimNextTriggerFor(ctx, fleet, time.Minute, nil, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("queue claim = %v, want the pinned trigger invisible", err)
	}
	if _, err := st.ClaimSpecificTriggerFor(ctx, "run-here", fleet, time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("fleet claim by id = %v, want refused", err)
	}
	claimed, err := st.ClaimSpecificTriggerFor(ctx, "run-here", here, time.Minute)
	if err != nil || claimed.ClaimToken == "" {
		t.Fatalf("here claim = %+v %v, want the claim and its token", claimed, err)
	}
	if _, err := st.ReleaseClaimAtGeneration(ctx, "run-here", claimed.ClaimSeq); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimSpecificTriggerFor(ctx, "run-here", here, time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second here claim = %v, want refused", err)
	}
	events, err := st.ListEventsAfter(ctx, "run-here", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		var p map[string]any
		if e.Kind == "runner_here" && json.Unmarshal(e.Payload, &p) == nil && p["reason"] == "INC-42" && p["token_prefix"] == tok.Prefix {
			return
		}
	}
	t.Fatalf("events = %+v, want a runner_here event with the reason and credential", events)
}
