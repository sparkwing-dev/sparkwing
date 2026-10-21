package store

import (
	"database/sql"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The credit ledger is written only by ledgerTx in ledger.go, which a team's ledger lock is the only way to obtain,
// so a second writer of credit_grants or credit_charges, which could skip the lock, fails here.
func TestLedgerTablesAreWrittenOnlyThroughTheLedger(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			text, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatalf("%s: %v", fset.Position(lit.Pos()), err)
			}
			if m := ledgerWrite.FindStringSubmatch(ledgerSQL(text)); m != nil {
				checked++
				if path != "ledger.go" {
					t.Errorf("%s writes %s outside ledger.go; write it through ledgerTx", fset.Position(lit.Pos()), m[2])
				}
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("found no ledger writes at all; the guard is not looking at the store")
	}
}

// Returning rows is still a mutation; each execution surface must refuse it before it reaches SQL.
func TestAssembledLedgerWritesAreRefusedAtRunTime(t *testing.T) {
	s := authorityStore(t)
	queryError := func(rows *sql.Rows, err error) error {
		if err != nil {
			return err
		}
		return errors.Join(rows.Err(), rows.Close())
	}
	assertRefused := func(t *testing.T, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "only through ledgerTx") {
			t.Fatalf("ledger mutation = %v; want refused by ledger boundary", err)
		}
	}
	for _, table := range []string{"credit_" + "grants", "credit_" + "charges"} {
		for _, target := range []string{table, strings.ToUpper(table), `"` + table + `"`, "`" + table + "`", "[" + table + "]", `main."` + table + `"`} {
			statements := []string{
				"DELETE FROM " + target,
				"uPdAtE " + target + " SET team=team",
				"UPDATE OR IGNORE " + target + " SET team=team",
				"UPDATE ONLY (" + target + ") SET team=team",
				"DELETE/* comment */FROM " + target,
				"DELETE -- comment\nFROM " + target,
				"DELETE /* outer /* inner */ outer */ FROM " + target,
				"WITH selected AS (SELECT 1) DELETE FROM " + target,
				"INSERT OR IGNORE INTO " + target + " SELECT * FROM " + table + " WHERE 0",
				"REPLACE INTO " + target + " SELECT * FROM " + table + " WHERE 0",
			}
			if strings.HasPrefix(target, `"`) || strings.HasPrefix(target, "`") || strings.HasPrefix(target, "[") {
				statements = append(statements, "UPDATE"+target+"SET team=team", "DELETE FROM"+target)
			}
			for _, statement := range statements {
				q := statement + " RETURNING team"
				t.Run(q, func(t *testing.T) {
					_, err := s.exec(t.Context(), q)
					assertRefused(t, err)
					_, err = s.execNoCtx(q)
					assertRefused(t, err)
					assertRefused(t, queryError(s.query(t.Context(), q)))
					assertRefused(t, queryError(s.queryNoCtx(q)))
					var team string
					assertRefused(t, s.queryRow(t.Context(), q).Scan(&team))
					assertRefused(t, s.queryRowNoCtx(q).Scan(&team))
					if err := s.inTx(t.Context(), func(tx *storeTx) error {
						_, err := tx.Exec(q)
						assertRefused(t, err)
						_, err = tx.ExecContext(t.Context(), q)
						assertRefused(t, err)
						assertRefused(t, queryError(tx.Query(q)))
						assertRefused(t, queryError(tx.QueryContext(t.Context(), q)))
						assertRefused(t, tx.QueryRow(q).Scan(&team))
						assertRefused(t, tx.QueryRowContext(t.Context(), q).Scan(&team))
						return nil
					}); err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
	for _, q := range []string{
		`SELECT 'UPDATE credit_grants SET created_by=''x'''`,
		`SELECT 1 /* DELETE FROM credit_charges */`,
		"SELECT 1 -- INSERT INTO credit_grants\n",
		`SELECT count(*) FROM credit_grants`,
	} {
		if err := queryError(s.query(t.Context(), q)); err != nil {
			t.Fatalf("read %q was refused: %v", q, err)
		}
	}
}

// PostgreSQL values can contain comment markers without turning the following statement into a comment.
func TestLedgerGuardPostgresQuotedValues(t *testing.T) {
	for _, q := range []string{
		`SELECT $$ /* $$; UPDATE credit_grants SET created_by=''`,
		`SELECT $value$ -- $value$; DELETE FROM credit_charges`,
		`SELECT E'\'--'; UPDATE credit_grants SET created_by=''`,
	} {
		if err := refuseLedgerWrite(q); err == nil {
			t.Fatalf("write after quoted value was allowed: %q", q)
		}
	}
	for _, q := range []string{
		`SELECT $$UPDATE credit_grants SET created_by=''$$`,
		`SELECT $value$DELETE FROM credit_charges$value$`,
		`SELECT E'\'DELETE FROM credit_charges'`,
	} {
		if err := refuseLedgerWrite(q); err != nil {
			t.Fatalf("quoted read was refused: %q: %v", q, err)
		}
	}
}

func TestLedgerGuardTruncateTableLists(t *testing.T) {
	for _, q := range []string{
		`TRUNCATE audit_events, credit_grants`,
		`TRUNCATE audit_events,credit_charges`,
		`TRUNCATE TABLE audit_events, ONLY public."credit_grants" RESTART IDENTITY CASCADE`,
		`TRUNCATE audit_events, payment_events, credit_charges, credit_grants`,
		`TRUNCATE teams CASCADE`,
		`SELECT 1; /* ledger rows cascade from teams */ TRUNCATE teams CASCADE`,
	} {
		if err := refuseLedgerWrite(q); err == nil {
			t.Fatalf("ledger mutation was allowed: %q", q)
		}
	}
	for _, q := range []string{
		`SELECT 'TRUNCATE teams CASCADE'`,
		`SELECT 1 /* TRUNCATE teams CASCADE */`,
	} {
		if err := refuseLedgerWrite(q); err != nil {
			t.Fatalf("quoted or commented truncate was refused: %q: %v", q, err)
		}
	}
}
