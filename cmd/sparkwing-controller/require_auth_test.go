package main

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// A multi-team license turns auth on over an empty tokens table, and
// --require-auth still refuses that start, because it promises a token.
func TestCheckRequireAuth_AMultiTeamControllerWithNoTokenRefusesToStart(t *testing.T) {
	srv, st := secretsTestServer(t, license.FeatureMultiTeam)
	srv.EnableAuthFromStore()
	if !srv.AuthEnabled() {
		t.Fatal("a multi-team controller with an empty tokens table left auth off")
	}
	if err := checkRequireAuth(st, true); err == nil {
		t.Fatal("--require-auth started a controller with an empty tokens table")
	}
	if err := checkRequireAuth(st, false); err != nil {
		t.Fatalf("without --require-auth the check refused: %v", err)
	}
	if _, _, err := st.CreateToken("root", store.TokenKindUser, []string{controller.ScopeAdmin}, 0, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := checkRequireAuth(st, true); err != nil {
		t.Fatalf("--require-auth refused a controller holding a token: %v", err)
	}
}
