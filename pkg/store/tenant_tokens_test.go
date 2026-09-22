package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestTenantTokens_ATeamsTokenCannotCarryTheOperatorScope(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	if err := st.AsOperator().CreateTeam(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	acme, err := st.ForTeam(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, _, err := acme.CreateToken(ctx, "acme-root", store.TokenKindUser,
		[]string{"runs.read", store.OperatorScope}, 0, now); !errors.Is(err, store.ErrOperatorScopeInTeam) {
		t.Errorf("minting the operator scope into acme = %v, want ErrOperatorScopeInTeam", err)
	}
	if _, _, err := acme.CreateToken(ctx, "acme-runner", store.TokenKindRunner,
		[]string{"nodes.claim"}, 0, now); err != nil {
		t.Errorf("minting a plain acme token: %v", err)
	}
	operator, err := st.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := operator.CreateToken(ctx, "root", store.TokenKindUser,
		[]string{store.OperatorScope}, 0, now); err != nil {
		t.Errorf("the operator's own team refused its admin token: %v", err)
	}
}
