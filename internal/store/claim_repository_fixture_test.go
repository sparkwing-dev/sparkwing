package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

func projectFixtureClaimRepositories(t *testing.T, db rewriteSchemaExecer, dialect Dialect, query string) {
	t.Helper()
	upper := strings.ToUpper(strings.TrimSpace(query))
	if !strings.HasPrefix(upper, "INSERT INTO RUNS") && !(strings.HasPrefix(upper, "UPDATE RUNS") && (strings.Contains(upper, "REPO_") || strings.Contains(upper, "REQUEST_JSON"))) {
		return
	}
	var rows rowCursor
	var err error
	switch reader := db.(type) {
	case *storeTx:
		rows, err = reader.queryContext(t.Context(), `SELECT id,repo_key,repo_display,request_json FROM runs`)
	case interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	}:
		rows, err = reader.QueryContext(t.Context(), `SELECT id,repo_key,repo_display,request_json FROM runs`)
	default:
		t.Fatal("run fixture writer cannot project claim identity")
	}
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		id, key, display string
		request          []byte
	}
	var runs []row
	for rows.Next() {
		var v row
		if err = rows.Scan(&v.id, &v.key, &v.display, &v.request); err != nil {
			t.Fatal(err)
		}
		runs = append(runs, v)
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, v := range runs {
		var sealed runspec.SealedRequest
		if err = json.Unmarshal(v.request, &sealed); err != nil {
			t.Fatal(err)
		}
		if v.display == "" {
			v.display = sealed.Request.Git.Repository.Display
		}
		identity, err := sourceurl.RoutingIdentity(v.key, v.display)
		if err != nil {
			identity = ""
		}
		if _, err = db.Exec(rewritePh(dialect, `UPDATE runs SET repo_identity=?,provenance_sha=?,provenance_ref=? WHERE id=?`), identity, sealed.Provenance.SHA(), sealed.Provenance.Ref(), v.id); err != nil {
			t.Fatal(err)
		}
	}
}
