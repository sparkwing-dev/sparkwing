package controller

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func FuzzRunIntakeRejectsLegacyEnvironmentProvenance(f *testing.F) {
	keys := []string{"SPARKWING_LEASE_TOKEN", "SPARKWING_CHILD_LEASE_TOKEN", "NPM_TOKEN", "SPARKWING_PG_URL", "_SPARKWING_RETRY_REPO_DIR", "_SPARKWING_RETRY_REPO_URL", "_SPARKWING_RETRY_REVISION", "_SPARKWING_RETRY_PLAN_HASH", "_SPARKWING_SUBMIT_PIPELINE_REV"}
	for i := range keys {
		f.Add(byte(i), []byte("private-value"))
	}
	f.Fuzz(func(t *testing.T, selector byte, value []byte) {
		if len(value) > 512 {
			t.Skip()
		}
		s, session := credentialSessionServer(t)
		options, _ := selectorClientOptions(t, s, session, `{"grants":["runs.write"]}`)
		raw, err := options.Bearer(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		secret := "private-input-" + hex.EncodeToString(value)
		for _, input := range []map[string]any{
			{"request": map[string]any{"version": 1, "team": "a", "pipeline": "ci", "env": map[string]string{keys[int(selector)%len(keys)]: secret}}},
			{"pipeline": "ci", "trigger": map[string]any{"source": "retry", "env": map[string]string{keys[int(selector)%len(keys)]: secret}}},
		} {
			body, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(http.MethodPost, "/api/v1/runs", bytes.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer "+raw)
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != http.StatusBadRequest || bytes.Contains(w.Body.Bytes(), []byte(secret)) {
				t.Fatalf("legacy provenance was admitted or disclosed: HTTP %d", w.Code)
			}
		}
		var count int
		if err := teststore.SQL(t, s.store).QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&count); err != nil || count != 1 {
			t.Fatalf("legacy intake wrote execution state: %d %v", count, err)
		}
	})
}
