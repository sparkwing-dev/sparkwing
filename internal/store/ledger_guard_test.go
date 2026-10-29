package store

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
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
	queryError := func(rows rowCursor, err error) error {
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
						_, err = tx.execContext(t.Context(), q)
						assertRefused(t, err)
						assertRefused(t, queryError(tx.query(q)))
						assertRefused(t, queryError(tx.queryContext(t.Context(), q)))
						assertRefused(t, tx.queryRow(q).Scan(&team))
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

// safety: each entry names a statement that reaches across payment modes on purpose,
// with its reason; the guard refuses an entry that no longer matches a statement.
var paymentModeAcrossModes = map[string]string{
	"(*ledgerTx).relabelCreator|credit_grants":             "account deletion relabels the creator on every mode's grants; no amount or balance is read",
	"(*Operator).grantCreatorTeamsTx|credit_grants":        "account deletion finds every team whose grants name the account, in any mode",
	"(*Operator).disputeHoldTx|credit_freezes":             "dispute_id is the table's primary key, and provider dispute ids never repeat across modes",
	"(*Tenant).ListNodes|credit_charges":                   "shows the cpu class of one award's reservation, which the award's prefix pins; no amount is read",
	"(*Tenant).GetNode|credit_charges":                     "shows the cpu class of one award's reservation, which the award's prefix pins; no amount is read",
	"(*Operator).ComputeUsage|credit_charges":              "counts running metered awards per principal, a capacity fact rather than money",
	"billingPriceTx|credit_charges":                        "reads one award's reservation by its id",
	"paidBillingTx|credit_charges":                         "settles one award against everything charged to it; cutover refuses to start while any award is unsettled, so an award's charges share one mode",
	"settledBillingResultTx|credit_charges":                "reports one award's billed seconds, in whatever mode they were charged",
	"(*Tenant).PrepareAuthorizedBillingPage|billing_pages": "reads the page's stored mode and refuses it unless it is the configured mode (configuredPaymentModeTx)",
	"(*Tenant).billingPageTx|billing_pages":                "reads the page's stored mode and refuses it unless it is the configured mode (configuredPaymentModeTx)",
}

// Decision 63 keeps test-mode money out of every live decision by one column, so
// every statement on a table that carries payment_mode must filter or set it;
// selecting the column alone does not count. The
// money tables come from the schema, so a new one is judged the day it is added.
func TestMoneyStatementsCarryPaymentMode(t *testing.T) {
	money := schemaTablesWithColumn(t, "payment_mode")
	if len(money) < 10 {
		t.Fatalf("schema lists only %d payment_mode tables: %v", len(money), money)
	}
	checked, used := 0, map[string]bool{}
	for _, stmt := range tenantSQLStatements(t) {
		tables := statementTables(stmt.text, money)
		if len(tables) == 0 {
			continue
		}
		checked++
		if bindsPaymentMode(stmt.text) {
			continue
		}
		if len(tables) == 1 {
			if _, ok := paymentModeAcrossModes[stmt.key+"|"+tables[0]]; ok {
				used[stmt.key+"|"+tables[0]] = true
				continue
			}
		}
		t.Errorf("%s: statement in %s touches %s without payment_mode.\n\t%s", stmt.pos, stmt.key, strings.Join(tables, ", "), collapse(stmt.text))
	}
	if checked < 50 {
		t.Fatalf("guard judged only %d money statements; it is no longer reading this package", checked)
	}
	for key, why := range paymentModeAcrossModes {
		if strings.TrimSpace(why) == "" || !used[key] {
			t.Errorf("paymentModeAcrossModes entry %q is stale or has no reason; delete it", key)
		}
	}
}

// paymentModeBinding matches payment_mode compared in a predicate, on either
// side, or written by an INSERT or SET; a statement that only selects the
// column does not bind it.
var paymentModeBinding = regexp.MustCompile(`(?is)\bpayment_mode\s*(=|<>|!=|\bin\b)|(=|<>|!=)\s*(\w+\.)?payment_mode\b|^\s*insert\b.*\bpayment_mode\b|\bset\b[^;]*\bpayment_mode\s*=`)

func bindsPaymentMode(text string) bool {
	return paymentModeBinding.MatchString(text)
}

func TestPaymentModeGuardRefusesAStatementThatOnlySelectsTheColumn(t *testing.T) {
	for q, binds := range map[string]bool{
		`SELECT payment_mode, amount FROM credit_grants WHERE team=?`:                   false,
		`SELECT amount FROM credit_grants WHERE team=? AND payment_mode=?`:              true,
		`SELECT amount FROM credit_grants g WHERE g.team=? AND g.payment_mode IN (?,?)`: true,
		`SELECT 1 FROM credit_grants g JOIN teams t ON t.payment_mode = g.payment_mode`: true,
		`INSERT INTO credit_grants(team,payment_mode,amount) VALUES (?,?,?)`:            true,
		`UPDATE credit_grants SET payment_mode=? WHERE id=?`:                            true,
		`UPDATE credit_grants SET amount=0 WHERE id=? RETURNING payment_mode`:           false,
	} {
		if got := bindsPaymentMode(q); got != binds {
			t.Errorf("bindsPaymentMode(%q) = %v, want %v", q, got, binds)
		}
	}
}
