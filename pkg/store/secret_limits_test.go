package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestASignedUpTeamsSecretsAreBounded(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	if err := st.AsOperator().CreateTeam(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	acme, err := st.ForTeam(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i := range store.MaxSecretsPerTeam {
		if err := acme.CreateOrReplaceSecret(store.Secret{Name: fmt.Sprintf("S%d", i), Value: "v"}, now); err != nil {
			t.Fatalf("secret %d: %v", i, err)
		}
	}
	if err := acme.CreateOrReplaceSecret(store.Secret{Name: "ONE_MORE", Value: "v"}, now); !errors.Is(err, store.ErrSecretLimit) {
		t.Fatalf("a secret past the cap = %v, want ErrSecretLimit", err)
	}
	// safety: negative control, replacing a row the team holds is not a new row.
	if err := acme.CreateOrReplaceSecret(store.Secret{Name: "S0", Value: "replaced"}, now); err != nil {
		t.Fatalf("replacing a held secret at the cap: %v", err)
	}
	if got, err := acme.GetSecret("S0"); err != nil || got.Value != "replaced" {
		t.Fatalf("S0 = %+v, %v; want it replaced", got, err)
	}
	big := strings.Repeat("x", store.MaxSecretValueBytes+1)
	if err := acme.CreateOrReplaceSecret(store.Secret{Name: "S1", Value: big}, now); !errors.Is(err, store.ErrSecretLimit) {
		t.Fatalf("an oversized value = %v, want ErrSecretLimit", err)
	}
	if got, err := acme.GetSecret("S1"); err != nil || got.Value != "v" {
		t.Fatalf("S1 = %+v, %v; want the refused write to leave it", got, err)
	}

	// safety: negative control, the operator's own team keeps no secret cap.
	for i := range store.MaxSecretsPerTeam + 1 {
		if err := st.CreateOrReplaceSecret(store.Secret{Name: fmt.Sprintf("OP%d", i), Value: "v"}, now); err != nil {
			t.Fatalf("operator secret %d: %v", i, err)
		}
	}
	if err := st.CreateOrReplaceSecret(store.Secret{Name: "OP_BIG", Value: big}, now); err != nil {
		t.Fatalf("an operator's large secret: %v", err)
	}
}
