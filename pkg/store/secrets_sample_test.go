package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestSampleSealedSecrets_ReturnsOnlySealedRowsWithTheirTeam(t *testing.T) {
	ctx := context.Background()
	s := storetest.Open(t)
	if err := s.AsOperator().TestOnlyCreateTeam(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	acme, err := s.ForTeam(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	if err := teststore.SeedSecret(s, store.DefaultTeam, store.Secret{Name: "PLAIN", Value: "plain", Principal: "seed"}, now); err != nil {
		t.Fatal(err)
	}
	if err := teststore.SeedSecret(s, acme.Team(), store.Secret{Name: "SEALED", Value: "enc:v3:abc", Principal: "seed"}, now); err != nil {
		t.Fatal(err)
	}
	got, err := s.SampleSealedSecrets(ctx, "enc:", 5)
	if err != nil {
		t.Fatalf("SampleSealedSecrets: %v", err)
	}
	if len(got) != 1 || got[0].Team != "acme" || got[0].Secret.Name != "SEALED" {
		t.Fatalf("sample = %+v, want acme's one sealed row", got)
	}
}
