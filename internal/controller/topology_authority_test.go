package controller

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func TestPlanTopologyStrangerLearnsNothingProperty(t *testing.T) {
	property := func(value []byte, terminal, missing, resultGrant bool) bool {
		s, _ := credentialSessionServer(t)
		private := "private-" + hex.EncodeToString(value)
		state := "running"
		var finished any
		if terminal {
			state = "failed"
			finished = int64(1)
		}
		engineSQL(t, s.store, `UPDATE runs SET error=?,pipeline='private-pipeline',status=?,finished_at=? WHERE team='a' AND id='run'`, private, state, finished)
		grants := []capability.Grant{capability.NodesClaim, capability.TriggersClaim}
		if resultGrant {
			grants = append(grants, capability.NodeResult)
		}
		raw := engineHTTPRoot(t, newEngineFixture(s.store, EngineController{}), "a", "stranger", grants)
		tenant, err := s.store.ForTeam(t.Context(), "a")
		if err != nil {
			t.Fatal(err)
		}
		before, err := tenant.ReadRun(t.Context(), "run")
		if err != nil {
			t.Fatal(err)
		}
		id := "run"
		want := http.StatusForbidden
		code := apierr.Forbidden
		if missing {
			id, want = "absent", http.StatusNotFound
			code = apierr.NotFound
		}
		r := httptest.NewRequest("POST", "/api/v1/runs/"+id+"/plan", strings.NewReader(`{"plan":{"jobs":[{"id":"injected"}]},"declarations":{"version":1,"file":"sparkwing.yaml"}}`))
		r.Header.Set("Authorization", "Bearer "+raw)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		var envelope apierr.Envelope
		if w.Code != want || json.Unmarshal(w.Body.Bytes(), &envelope) != nil || envelope.Error.Code != code {
			t.Logf("stranger plan: HTTP %d, want %d: %s", w.Code, want, w.Body)
			return false
		}
		for _, secret := range []string{private, "private-pipeline", "failed", raw} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatalf("stranger learned %q: %s", secret, w.Body)
			}
		}
		after, err := tenant.ReadRun(t.Context(), "run")
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("stranger changed run: %+v: %v", after, err)
		}
		var rows int
		if err := teststore.SQL(t, s.store).QueryRow(`SELECT (SELECT COUNT(*) FROM nodes)+(SELECT COUNT(*) FROM events)+(SELECT COUNT(*) FROM run_effects)`).Scan(&rows); err != nil || rows != 0 {
			t.Fatalf("stranger persisted topology or effects=%d: %v", rows, err)
		}
		return true
	}
	for _, terminal := range []bool{false, true} {
		for _, missing := range []bool{false, true} {
			for _, grant := range []bool{false, true} {
				if !property([]byte("private run error"), terminal, missing, grant) {
					t.Fatal("stranger reached plan acceptance")
				}
			}
		}
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 20}); err != nil {
		t.Fatal(err)
	}
}
