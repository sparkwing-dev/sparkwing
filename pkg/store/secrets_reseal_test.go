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

// A pass reaches every team's rows through more than one batch, leaves rows
// already sealed alone, and keeps a row the reseal function refuses as it
// was. A second pass finds only the refused row.
func TestResealSecrets_WalksEveryTeamInBatchesAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := storetest.Open(t)
	if err := s.AsOperator().CreateTeam(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	acme, err := s.ForTeam(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	type row struct {
		tn       *store.Tenant
		name     string
		pipeline string
		value    string
	}
	def, err := s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	rows := []row{
		{def, "A", "", "plain-a"},
		{def, "B", "deploy", "plain-b"},
		{def, "C", "", "sealed:c"},
		{def, "BROKEN", "", "plain-broken"},
		{acme, "A", "", "plain-acme-a"},
		{acme, "A", "deploy", "plain-acme-a-deploy"},
		{acme, "Z", "", "sealed:z"},
	}
	for _, r := range rows {
		if err := r.tn.CreateOrReplaceSecret(store.Secret{
			Name: r.name, Value: r.value, Principal: "seed", Pipeline: r.pipeline, Masked: true,
		}, now); err != nil {
			t.Fatalf("seed %s/%s/%s: %v", r.tn.Team(), r.name, r.pipeline, err)
		}
	}

	var seen []string
	reseal := func(team store.Team, sec store.Secret) (string, error) {
		seen = append(seen, string(team)+"/"+sec.Name+"/"+sec.Pipeline)
		if strings.HasPrefix(sec.Value, "sealed:") {
			t.Errorf("reseal was handed a row already sealed: %s/%s", team, sec.Name)
		}
		if sec.Name == "BROKEN" {
			return "", errors.New("does not open")
		}
		return "sealed:" + string(team) + ":" + sec.Value, nil
	}
	counts, err := s.ResealSecrets(ctx, "sealed:", 2, reseal)
	if err != nil {
		t.Fatalf("ResealSecrets: %v", err)
	}
	if counts.Resealed != 4 || counts.Skipped != 1 || counts.Raced != 0 {
		t.Fatalf("first pass counts = %+v, want 4 resealed and 1 skipped", counts)
	}
	if len(seen) != 5 {
		t.Fatalf("reseal saw %d rows (%v), want the 5 unsealed ones", len(seen), seen)
	}

	for _, want := range []struct {
		tn             *store.Tenant
		name, pipeline string
		value          string
	}{
		{def, "A", "", "sealed:default:plain-a"},
		{def, "B", "deploy", "sealed:default:plain-b"},
		{def, "C", "", "sealed:c"},
		{def, "BROKEN", "", "plain-broken"},
		{acme, "A", "", "sealed:acme:plain-acme-a"},
		{acme, "A", "deploy", "sealed:acme:plain-acme-a-deploy"},
		{acme, "Z", "", "sealed:z"},
	} {
		got, err := want.tn.GetSecretRow(want.name, want.pipeline)
		if err != nil {
			t.Fatalf("read %s/%s/%s: %v", want.tn.Team(), want.name, want.pipeline, err)
		}
		if got.Value != want.value {
			t.Errorf("%s/%s/%s = %q, want %q", want.tn.Team(), want.name, want.pipeline, got.Value, want.value)
		}
		if !got.UpdatedAt.Equal(now) {
			t.Errorf("%s/%s/%s updated_at moved to %v; a reseal does not change the secret", want.tn.Team(), want.name, want.pipeline, got.UpdatedAt)
		}
	}

	seen = nil
	counts, err = s.ResealSecrets(ctx, "sealed:", 2, reseal)
	if err != nil {
		t.Fatalf("second ResealSecrets: %v", err)
	}
	if counts.Resealed != 0 || counts.Skipped != 1 || len(seen) != 1 {
		t.Fatalf("second pass counts = %+v over %v, want only the refused row", counts, seen)
	}
}

// A row rewritten between the pass reading it and writing it keeps the newer
// value, because the write names the value it replaces.
func TestResealSecrets_LeavesARowRewrittenMeanwhile(t *testing.T) {
	ctx := context.Background()
	s := storetest.Open(t)
	now := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	if err := s.CreateOrReplaceSecret(store.Secret{Name: "TOKEN", Value: "plain-old", Principal: "seed", Masked: true}, now); err != nil {
		t.Fatal(err)
	}
	counts, err := s.ResealSecrets(ctx, "sealed:", 10, func(_ store.Team, sec store.Secret) (string, error) {
		if err := s.CreateOrReplaceSecret(store.Secret{Name: "TOKEN", Value: "sealed:newer", Principal: "writer", Masked: true}, now); err != nil {
			t.Fatalf("concurrent write: %v", err)
		}
		return "sealed:" + sec.Value, nil
	})
	if err != nil {
		t.Fatalf("ResealSecrets: %v", err)
	}
	if counts.Raced != 1 || counts.Resealed != 0 {
		t.Fatalf("counts = %+v, want the one row counted raced", counts)
	}
	got, err := s.GetSecret("TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != "sealed:newer" {
		t.Fatalf("value = %q, want the concurrent writer's", got.Value)
	}
}

func TestSampleSealedSecrets_ReturnsOnlySealedRowsWithTheirTeam(t *testing.T) {
	ctx := context.Background()
	s := storetest.Open(t)
	if err := s.AsOperator().CreateTeam(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	acme, err := s.ForTeam(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	if err := s.CreateOrReplaceSecret(store.Secret{Name: "PLAIN", Value: "plain", Principal: "seed"}, now); err != nil {
		t.Fatal(err)
	}
	if err := acme.CreateOrReplaceSecret(store.Secret{Name: "SEALED", Value: "enc:v3:abc", Principal: "seed"}, now); err != nil {
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
