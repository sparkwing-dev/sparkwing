package store

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

func projectFixtureClaimRepositories(t *testing.T, db rewriteSchemaExecer, dialect Dialect, query string) {
	t.Helper()
	upper := strings.ToUpper(strings.TrimSpace(query))
	if !strings.HasPrefix(upper, "INSERT INTO RUNS") && !(strings.HasPrefix(upper, "UPDATE RUNS") && strings.Contains(upper, "REPO_")) {
		return
	}
	reader, ok := db.(interface {
		Query(string, ...any) (*sql.Rows, error)
	})
	if !ok {
		t.Fatal("run fixture writer cannot project claim identity")
	}
	rows, err := reader.Query(`SELECT id,repo_key,repo_display,request_json FROM runs`)
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
		if v.display == "" {
			var request struct {
				Request struct {
					Git struct {
						Repository struct {
							Display string `json:"display"`
						} `json:"repository"`
					} `json:"git"`
				} `json:"request"`
			}
			if err = json.Unmarshal(v.request, &request); err != nil {
				t.Fatal(err)
			}
			v.display = request.Request.Git.Repository.Display
		}
		identity, err := sourceurl.RoutingIdentity(v.key, v.display)
		if err != nil {
			identity = ""
		}
		if _, err = db.Exec(rewritePh(dialect, `UPDATE runs SET repo_identity=? WHERE id=?`), identity, v.id); err != nil {
			t.Fatal(err)
		}
	}
}
