package store

import (
	"database/sql"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// TestStoreSQLTrustLiterals keeps every SQL trust list generated from the runspec
// trust sets, so one rule change cannot leave a stale hand-written copy behind.
func TestStoreSQLTrustLiterals(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			for _, class := range runspec.AllTrust {
				if strings.Contains(lit.Value, `'`+string(class)+`'`) {
					t.Errorf("%s: trust class %q written into SQL by hand; render it with trustIn and a runspec trust set", fset.Position(lit.Pos()), class)
				}
			}
			return true
		})
	}
}

// TestDefaultBranchWorkSQLAgreesWithGo evaluates the SQL rule against every trust
// class, including a verified delivery on the default branch that is not a push,
// which the fresh-pod SQL counted while the pool SQL and the Go cache rule did not.
// Intake seals every signed default-branch push as verified-default, so the
// non-push case is ordinary verified work everywhere.
func TestDefaultBranchWorkSQLAgreesWithGo(t *testing.T) {
	sha := strings.Repeat("a", 40)
	app := func(event string, push bool) runspec.Provenance {
		p, err := runspec.AppProvenance(runspec.AppFacts{Event: event, RepositoryID: 7, Ref: "refs/heads/main", SHA: sha, DefaultBranch: "main", DefaultBranchPush: push, DeliveryKey: "delivery", VerifiedAt: time.Unix(1, 0)}, runspec.Optional[runspec.PullRequest]{}, "ci")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	schedule, err := runspec.ScheduleProvenance("nightly", 7, "main", sha, "ci")
	if err != nil {
		t.Fatal(err)
	}
	actions, err := runspec.ActionsProvenance(7, "refs/heads/main", sha, "ci")
	if err != nil {
		t.Fatal(err)
	}
	asserted, err := runspec.AssertedProvenance(runspec.PrincipalWriter("account", "a"), "refs/heads/main", sha, "ci")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]runspec.Provenance{"default push": app("push", true), "default create": app("create", false), "schedule": schedule, "actions": actions, "asserted": asserted}
	if cases["default create"].DefaultBranchWork() || !cases["default push"].DefaultBranchWork() || !cases["schedule"].DefaultBranchWork() || cases["actions"].DefaultBranchWork() {
		t.Fatal("verified default-branch work is a signed default-branch push or an admin-armed schedule")
	}
	for _, dialect := range []Dialect{DialectSQLite, DialectPostgres} {
		t.Run(dialect.String(), func(t *testing.T) {
			driver, dsn := "sqlite", ":memory:"
			if dialect == DialectPostgres {
				driver, dsn = "pgx", os.Getenv("SPARKWING_TEST_PG_URL")
				if dsn == "" {
					t.Skip("PostgreSQL trust test requires SPARKWING_TEST_PG_URL")
				}
			}
			db, err := sql.Open(driver, dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			q := claimSQL{dialect: dialect}
			query := rewritePh(dialect, `WITH p(value) AS (SELECT CAST(? AS TEXT)) SELECT `+q.defaultBranchWork("p.value")+` FROM p`)
			for name, p := range cases {
				raw, err := json.Marshal(p)
				if err != nil {
					t.Fatal(err)
				}
				var got bool
				if err := db.QueryRowContext(t.Context(), query, string(raw)).Scan(&got); err != nil {
					t.Fatal(err)
				}
				if got != p.DefaultBranchWork() {
					t.Errorf("%s: SQL default-branch work = %v, Go = %v", name, got, p.DefaultBranchWork())
				}
			}
		})
	}
}
