package store

import (
	"errors"
	"testing"
	"time"
)

func TestLocalDashboardCodesRedeemOnceAtTheirOriginIntoStoredSessions(t *testing.T) {
	s := localRunnerStore(t)
	operator, ctx := s.AsOperator(), t.Context()
	now := time.Unix(1_800_000_000, 0)
	month := now.Add(30 * 24 * time.Hour)
	const origin, peer = "http://127.0.0.1:4343", `{"GOOS":"linux","UID":7}`
	for _, code := range []string{"code-a", "code-b", "code-c"} {
		if err := operator.MintLocalDashboardCode(ctx, code, origin, peer, now, now.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := operator.RedeemLocalDashboardCode(ctx, "code-a", "session-a", "http://127.0.0.1:9999", now, month); err == nil {
		t.Fatal("redeemed a code at another origin")
	}
	if _, err := operator.RedeemLocalDashboardCode(ctx, "code-a", "session-a", origin, now, month); err == nil {
		t.Fatal("a code refused at the wrong origin stayed redeemable")
	}
	if _, err := operator.RedeemLocalDashboardCode(ctx, "code-b", "session-b", origin, now.Add(2*time.Minute), month); err == nil {
		t.Fatal("redeemed an expired code")
	}
	got, err := operator.RedeemLocalDashboardCode(ctx, "code-c", "session-c", origin, now, month)
	if err != nil || got != peer {
		t.Fatalf("redeem: %q %v", got, err)
	}
	if _, err := operator.RedeemLocalDashboardCode(ctx, "code-c", "session-d", origin, now, month); err == nil {
		t.Fatal("redeemed a code twice")
	}
	if _, err := operator.LocalDashboardSessionAt(ctx, "code-c", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a code digest read as a session: %v", err)
	}
	session, err := operator.LocalDashboardSessionAt(ctx, "session-c", now.Add(29*24*time.Hour))
	if err != nil || session.Origin != origin || session.Peer != peer || !session.ExpiresAt.Equal(month) {
		t.Fatalf("session: %+v %v", session, err)
	}
	if _, err := operator.LocalDashboardSessionAt(ctx, "session-c", month); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a session outlived its expiry: %v", err)
	}
	if live, err := operator.RevokeLocalDashboardSession(ctx, "session-c", now); err != nil || !live {
		t.Fatalf("revoke: %t %v", live, err)
	}
	if _, err := operator.LocalDashboardSessionAt(ctx, "session-c", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a revoked session still reads: %v", err)
	}
	if live, err := operator.RevokeLocalDashboardSession(ctx, "session-c", now); err != nil || live {
		t.Fatalf("second revoke: %t %v", live, err)
	}
}
