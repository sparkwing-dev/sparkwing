package store_test

import (
	"encoding/hex"
	"net/url"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestSchemaBaselineDigestRecordsAndReopens(t *testing.T) {
	target := storetest.New(t)
	st := target.Open(t)
	digest := readSchemaBaselineDigest(t, st)
	if decoded, err := hex.DecodeString(digest); err != nil || len(decoded) != 32 {
		t.Fatalf("fresh baseline digest = %q, want SHA-256", digest)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if got := readSchemaBaselineDigest(t, target.Open(t)); got != digest {
		t.Fatalf("reopened baseline digest = %q, want %q", got, digest)
	}
}

func TestSchemaBaselineDigestRefusesChangedBaseline(t *testing.T) {
	target := storetest.New(t)
	st := target.Open(t)
	expected := readSchemaBaselineDigest(t, st)
	recorded := strings.Repeat("0", 64)
	if _, err := st.DB().Exec(storetest.Rebind(st, `UPDATE sparkwing_schema_version SET baseline_digest = ?`), recorded); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := target.TryOpen()
	if err == nil {
		if closeErr := reopened.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
		t.Fatal("opened a store from a different baseline at the same version")
	}
	for _, want := range []string{recorded, expected, "back up the store", "this build cannot upgrade it", "to start fresh,"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q omits %q", err, want)
		}
	}
	remedy := "move "
	if st.Dialect() == store.DialectPostgres {
		remedy = "point the server at an empty database"
	}
	if !strings.Contains(err.Error(), remedy) {
		t.Errorf("refusal %q omits remedy %q", err, remedy)
	}
}

func TestSchemaBaselineDigestLegacy(t *testing.T) {
	for _, missing := range []string{"", "TABLE source_uploads", "VIEW warm_pool_desired", "INDEX idx_runs_push_claim"} {
		t.Run("missing_"+missing, func(t *testing.T) {
			target := storetest.New(t)
			st := target.Open(t)
			expected := readSchemaBaselineDigest(t, st)
			if _, err := st.DB().Exec(`ALTER TABLE sparkwing_schema_version DROP COLUMN baseline_digest`); err != nil {
				t.Fatal(err)
			}
			if missing != "" {
				if _, err := st.DB().Exec(`DROP ` + missing); err != nil {
					t.Fatal(err)
				}
			}
			reopened, err := target.TryOpen()
			if missing != "" {
				if err == nil {
					if closeErr := reopened.Close(); closeErr != nil {
						t.Fatal(closeErr)
					}
					t.Fatal("opened an incomplete legacy baseline")
				}
				for _, want := range []string{"<missing>", expected, "missing baseline object", strings.ToLower(missing), "back up the store", "to start fresh,"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("refusal %q omits %q", err, want)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if closeErr := reopened.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
			// safety: a separate open proves the legacy stamp committed.
			if got := readSchemaBaselineDigest(t, target.Open(t)); got != expected {
				t.Fatalf("legacy baseline digest = %q, want %q", got, expected)
			}
		})
	}
}

func TestSchemaBaselineDigestLegacyEmpty(t *testing.T) {
	target := storetest.New(t)
	st := target.Open(t)
	expected := readSchemaBaselineDigest(t, st)
	if _, err := st.DB().Exec(`UPDATE sparkwing_schema_version SET baseline_digest = ''`); err != nil {
		t.Fatal(err)
	}
	if got := readSchemaBaselineDigest(t, target.Open(t)); got != expected {
		t.Fatalf("legacy baseline digest = %q, want %q", got, expected)
	}
}

func TestOpenExistingSchemaBaselineDigest(t *testing.T) {
	for _, state := range []string{"matching", "different", "legacy", "missing"} {
		t.Run(state, func(t *testing.T) {
			target := storetest.New(t)
			st := target.Open(t)
			location := target.DSN()
			if st.Dialect() == store.DialectSQLite {
				id, err := st.Connection(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				location = strings.TrimPrefix(id.Target, "sqlite:")
			}
			expected := readSchemaBaselineDigest(t, st)
			var statements []string
			switch state {
			case "different":
				statements = []string{`UPDATE sparkwing_schema_version SET baseline_digest = 'different'`}
			case "legacy", "missing":
				statements = []string{`ALTER TABLE sparkwing_schema_version DROP COLUMN baseline_digest`}
				if state == "missing" {
					statements = append(statements, `DROP VIEW warm_pool_desired`)
				}
			}
			for _, statement := range statements {
				if _, err := st.DB().Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			reopened, err := store.OpenExisting(t.Context(), st.Dialect(), location)
			if err == nil {
				defer func() { _ = reopened.Close() }()
			}
			if state == "different" || state == "missing" {
				if err == nil || !strings.Contains(err.Error(), expected) || !strings.Contains(err.Error(), "back up the store") {
					t.Fatalf("OpenExisting error = %v, want baseline refusal and remedy", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := readSchemaBaselineDigest(t, st); got != expected {
				t.Fatalf("baseline digest = %q, want %q", got, expected)
			}
		})
	}
}

func TestSchemaBaselinePostgresSearchPath(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		target := storetest.NewPostgres(t)
		st := target.Open(t)
		expected := readSchemaBaselineDigest(t, st)
		if legacy {
			if _, err := st.DB().Exec(`ALTER TABLE sparkwing_schema_version DROP COLUMN baseline_digest`); err != nil {
				t.Fatal(err)
			}
		}
		empty, _ := url.Parse(storetest.NewPostgres(t).DSN())
		u, _ := url.Parse(target.DSN())
		q := u.Query()
		q.Set("search_path", empty.Query().Get("search_path")+","+q.Get("search_path"))
		u.RawQuery = q.Encode()
		reopened, err := store.OpenPostgres(t.Context(), u.String())
		if err != nil {
			t.Fatal(err)
		}
		if err := reopened.Close(); err != nil {
			t.Fatal(err)
		}
		if got := readSchemaBaselineDigest(t, st); got != expected {
			t.Fatalf("baseline digest = %q, want %q", got, expected)
		}
	}
}

func readSchemaBaselineDigest(t *testing.T, st *store.Store) string {
	t.Helper()
	var digest string
	if err := st.DB().QueryRow(`SELECT baseline_digest FROM sparkwing_schema_version`).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	return digest
}
