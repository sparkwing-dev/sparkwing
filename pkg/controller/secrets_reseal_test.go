package controller_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type rawSecretRow struct {
	team, name, pipeline, value string
}

// safety: the rows are read with SQL rather than through the store, so the
// check is what a reader of the database itself would see.
func rawSecretRows(t *testing.T, st *store.Store) map[string]rawSecretRow {
	t.Helper()
	rows, err := st.DB().QueryContext(context.Background(), `SELECT team, name, pipeline, value FROM secrets`)
	if err != nil {
		t.Fatalf("select secrets: %v", err)
	}
	defer rows.Close()
	out := map[string]rawSecretRow{}
	for rows.Next() {
		var r rawSecretRow
		if err := rows.Scan(&r.team, &r.name, &r.pipeline, &r.value); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[r.team+"/"+r.name+"/"+r.pipeline] = r
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

func TestResealStoredSecrets_SealsEveryTeamsRowsOnBothDialects(t *testing.T) {
	for _, tc := range []struct {
		name string
		open func(*testing.T) *store.Store
	}{
		{name: "sqlite", open: openSQLiteBindingStore},
		{name: "postgres", open: openPostgresBindingStore},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st := tc.open(t)
			if err := st.AsOperator().CreateTeam(ctx, "acme"); err != nil {
				t.Fatal(err)
			}
			acme, err := st.ForTeam(ctx, "acme")
			if err != nil {
				t.Fatal(err)
			}
			key, _ := secrets.GenerateKey()
			c, _ := secrets.NewCipher(key)
			legacy, _ := c.Seal("legacy-value")
			bound, _ := c.SealBound("default", "BOUND", "", false, true, "bound-value")
			now := time.Now().UTC()
			for _, seed := range []struct {
				write          func(store.Secret, time.Time) error
				name, pipeline string
				value          string
			}{
				{st.CreateOrReplaceSecret, "PLAIN", "", "default-plain"},
				{st.CreateOrReplaceSecret, "PLAIN", "deploy", "default-deploy-plain"},
				{st.CreateOrReplaceSecret, "LEGACY", "", legacy},
				{st.CreateOrReplaceSecret, "BOUND", "", bound},
				{acme.CreateOrReplaceSecret, "PLAIN", "", "acme-plain"},
				// safety: a pre-team envelope copied out of the default team, which the reseal must refuse.
				{acme.CreateOrReplaceSecret, "LEGACY", "", legacy},
			} {
				if err := seed.write(store.Secret{
					Name: seed.name, Value: seed.value, Principal: "seed", Pipeline: seed.pipeline, Masked: true,
				}, now); err != nil {
					t.Fatalf("seed %s: %v", seed.name, err)
				}
			}

			srv := controller.New(st, nil).WithSecretsCipher(c)
			counts, err := srv.ResealStoredSecrets(ctx)
			if err != nil {
				t.Fatalf("ResealStoredSecrets: %v", err)
			}
			if counts.Resealed != 4 || counts.Skipped != 1 || counts.Raced != 0 {
				t.Fatalf("counts = %+v, want 4 resealed and the copied envelope skipped", counts)
			}

			raw := rawSecretRows(t, st)
			for key, want := range map[string]string{
				"default/PLAIN/":       "default-plain",
				"default/PLAIN/deploy": "default-deploy-plain",
				"default/LEGACY/":      "legacy-value",
				"default/BOUND/":       "bound-value",
				"acme/PLAIN/":          "acme-plain",
			} {
				row, ok := raw[key]
				if !ok {
					t.Fatalf("row %s missing", key)
				}
				if !strings.HasPrefix(row.value, secrets.BoundPrefix) {
					t.Errorf("row %s is stored as %.12q..., want a team-bound envelope", key, row.value)
				}
				if strings.Contains(row.value, want) {
					t.Errorf("row %s holds its plaintext in the database", key)
				}
				plain, err := c.OpenBound(row.team, row.name, row.pipeline, false, true, row.value)
				if err != nil || plain != want {
					t.Errorf("row %s opens to %q, %v; want %q", key, plain, err, want)
				}
			}
			if raw["default/BOUND/"].value != bound {
				t.Error("a row already sealed to its team was rewritten")
			}
			if raw["acme/LEGACY/"].value != legacy {
				t.Error("a pre-team envelope outside the default team was resealed into acme")
			}

			again, err := srv.ResealStoredSecrets(ctx)
			if err != nil {
				t.Fatalf("second ResealStoredSecrets: %v", err)
			}
			if again.Resealed != 0 || again.Skipped != 1 {
				t.Fatalf("second pass counts = %+v, want nothing left but the refused row", again)
			}
		})
	}
}

// A key other than the one the table was sealed under must stop the reseal
// before it seals plaintext rows under it, and reads under it fail rather
// than answer empty.
func TestResealStoredSecrets_RefusesAKeyThatOpensNothingStored(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteBindingStore(t)
	rightKey, _ := secrets.GenerateKey()
	wrongKey, _ := secrets.GenerateKey()
	right, _ := secrets.NewCipher(rightKey)
	wrong, _ := secrets.NewCipher(wrongKey)
	sealed, _ := right.SealBound("default", "TOKEN", "", false, true, "supersecret")
	now := time.Now().UTC()
	if err := st.CreateOrReplaceSecret(store.Secret{Name: "TOKEN", Value: sealed, Principal: "seed", Masked: true}, now); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateOrReplaceSecret(store.Secret{Name: "LATER", Value: "written-without-a-key", Principal: "seed", Masked: true}, now); err != nil {
		t.Fatal(err)
	}

	srv := controller.New(st, nil).WithSecretsCipher(wrong)
	if _, err := srv.ResealStoredSecrets(ctx); err == nil || !strings.Contains(err.Error(), "opens none") {
		t.Fatalf("ResealStoredSecrets under the wrong key err = %v, want a refusal naming the key", err)
	}
	if got := rawSecretRows(t, st)["default/LATER/"].value; got != "written-without-a-key" {
		t.Fatalf("a refused reseal still rewrote the plaintext row to %.12q...", got)
	}

	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	status, body := getSecretStatus(t, hs.URL+"/api/v1/secrets/TOKEN")
	if status != http.StatusInternalServerError {
		t.Fatalf("GET under the wrong key status = %d, want 500, body = %s", status, body)
	}
	if strings.Contains(body, "supersecret") || strings.Contains(body, `"value"`) {
		t.Fatalf("GET under the wrong key answered a value: %s", body)
	}

	if _, err := controller.New(st, nil).WithSecretsCipher(right).ResealStoredSecrets(ctx); err != nil {
		t.Fatalf("ResealStoredSecrets under the right key: %v", err)
	}
}

// Envelopes the probe cannot judge prove nothing about the key, so a table
// that holds envelopes but none the probe can open or fail is refused rather
// than resealed: the reseal would seal its plaintext rows under a key nobody
// checked.
func TestResealStoredSecrets_RefusesWhenNoSampledEnvelopeCanJudgeTheKey(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteBindingStore(t)
	if err := st.AsOperator().CreateTeam(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	acme, err := st.ForTeam(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	rightKey, _ := secrets.GenerateKey()
	wrongKey, _ := secrets.GenerateKey()
	right, _ := secrets.NewCipher(rightKey)
	wrong, _ := secrets.NewCipher(wrongKey)
	legacy, _ := right.Seal("legacy-value")
	now := time.Now().UTC()
	if err := acme.CreateOrReplaceSecret(store.Secret{Name: "COPIED", Value: legacy, Principal: "seed", Masked: true}, now); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateOrReplaceSecret(store.Secret{Name: "LATER", Value: "written-without-a-key", Principal: "seed", Masked: true}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.New(st, nil).WithSecretsCipher(wrong).ResealStoredSecrets(ctx); err == nil {
		t.Fatal("ResealStoredSecrets proceeded with a key no sampled envelope could judge")
	}
	if got := rawSecretRows(t, st)["default/LATER/"].value; got != "written-without-a-key" {
		t.Fatalf("a refused reseal still rewrote the plaintext row to %.12q...", got)
	}
}

// One team's envelope written into another team's row, by anyone who can
// write the database, does not open for that team's runner.
func TestSecrets_EnvelopeMovedToAnotherTeamDoesNotOpen(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteBindingStore(t)
	if err := st.AsOperator().CreateTeam(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	acme, err := st.ForTeam(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	key, _ := secrets.GenerateKey()
	c, _ := secrets.NewCipher(key)
	now := time.Now().UTC()
	defaultSealed, _ := c.SealBound("default", "API_KEY", "deploy", false, true, "default-team-value")
	acmeSealed, _ := c.SealBound("acme", "API_KEY", "deploy", false, true, "acme-value")
	if err := st.CreateOrReplaceSecret(store.Secret{
		Name: "API_KEY", Value: defaultSealed, Principal: "root", Pipeline: "deploy", Masked: true,
	}, now); err != nil {
		t.Fatal(err)
	}
	writeAcme := func(value string) {
		t.Helper()
		if err := acme.CreateOrReplaceSecret(store.Secret{
			Name: "API_KEY", Value: value, Principal: "root", Pipeline: "deploy", Masked: true,
		}, now); err != nil {
			t.Fatal(err)
		}
	}
	writeAcme(acmeSealed)
	if err := acme.CreateTriggerWithRun(ctx,
		store.Trigger{ID: "run-acme", Pipeline: "deploy", CreatedAt: now},
		store.Run{ID: "run-acme", Pipeline: "deploy", Status: "pending", StartedAt: now},
	); err != nil {
		t.Fatal(err)
	}
	raw, _, err := acme.CreateToken(ctx, "agent:acme", store.TokenKindRunner, runnerScopes, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().WithSecretsCipher(c).Handler())
	t.Cleanup(hs.Close)
	cl := client.NewWithToken(hs.URL, nil, raw)
	if _, err := cl.ClaimSpecificTrigger(ctx, "run-acme", time.Minute); err != nil {
		t.Fatalf("ClaimSpecificTrigger: %v", err)
	}

	sec, err := cl.GetSecretForRun(ctx, "API_KEY", "run-acme")
	if err != nil || sec.Value != "acme-value" {
		t.Fatalf("acme's own envelope read = %v, %v; want acme-value", sec, err)
	}

	writeAcme(defaultSealed)
	sec, err = cl.GetSecretForRun(ctx, "API_KEY", "run-acme")
	if err == nil {
		t.Fatalf("the default team's envelope in acme's row opened for acme's runner: %q", sec.Value)
	}
	if strings.Contains(err.Error(), "default-team-value") {
		t.Fatalf("the refused read leaked the moved value: %v", err)
	}
}
