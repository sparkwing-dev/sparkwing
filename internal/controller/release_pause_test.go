package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func pauseHTTPFixture(t *testing.T) (*Server, string) {
	t.Helper()
	s, session := credentialSessionServer(t)
	engineSQL(t, s.store, `UPDATE sessions SET scopes='runs.read,runs.control'`)
	engineSQL(t, s.store, `INSERT INTO nodes(team,run_id,node_id,status,status_detail,spec_json,execution_json) VALUES ('a','run','job','pending','paused',?,?)`, []byte(`{"id":"job"}`), []byte(`{}`))
	engineSQL(t, s.store, `INSERT INTO debug_pauses(team,run_id,node_id,reason,paused_at,expires_at) VALUES ('a','run','job','manual',1,?)`, time.Now().Add(time.Hour).UnixNano())
	return s, session
}

func resumeHTTP(s *Server, session, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "https://controller.test"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://controller.test")
	r.Header.Set(csrfHeaderName, "session-csrf")
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "session-csrf"})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestResumeDebugPauseCommitsPrincipalAndReplays(t *testing.T) {
	s, session := pauseHTTPFixture(t)
	for range 2 {
		w := resumeHTTP(s, session, "/api/v1/runs/run/jobs/job/resume", `{}`)
		if w.Code != 204 {
			t.Fatalf("resume HTTP %d: %s", w.Code, w.Body)
		}
	}
	tenant, err := s.store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	pauses, err := tenant.ListDebugPauses(t.Context(), "run")
	if err != nil || len(pauses) != 1 || pauses[0].ReleasedAt == nil || pauses[0].ReleasedBy != "member" || pauses[0].ReleaseKind != store.PauseReleaseManual {
		t.Fatalf("pauses=%+v err=%v", pauses, err)
	}
	node, err := tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: "run", NodeID: "job"})
	if err != nil || node.Status != store.StatusPending || node.StatusDetail != "" {
		t.Fatalf("job=%+v err=%v", node, err)
	}
	var count int
	if err := teststore.SQL(t, s.store).QueryRow(`SELECT COUNT(*) FROM events WHERE kind='debug.resumed'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("events=%d err=%v", count, err)
	}
}

func TestResumeDebugPauseRejectsForgedActorsAndDeadCredentials(t *testing.T) {
	s, session := pauseHTTPFixture(t)
	if w := resumeHTTP(s, "", "/api/v1/runs/run/jobs/job/resume", `{}`); w.Code != 401 {
		t.Fatalf("unauthenticated HTTP %d %s", w.Code, w.Body)
	}
	for _, body := range []string{`{"released_by":"other"}`, `{"team":"b"}`, `{"release_kind":"manual"}`, `null`, ``} {
		if w := resumeHTTP(s, session, "/api/v1/runs/run/jobs/job/resume", body); w.Code != 400 {
			t.Fatalf("body %q: HTTP %d %s", body, w.Code, w.Body)
		}
	}
	if w := resumeHTTP(s, session, "/api/v1/runs/run/jobs/missing/resume", `{}`); w.Code != 404 {
		t.Fatalf("missing HTTP %d %s", w.Code, w.Body)
	}
	digest := sha256.Sum256([]byte(session))
	engineSQL(t, s.store, `UPDATE sessions SET revoked_at=1 WHERE hash=?`, hex.EncodeToString(digest[:]))
	if w := resumeHTTP(s, session, "/api/v1/runs/run/jobs/job/resume", `{}`); w.Code != 401 {
		t.Fatalf("revoked HTTP %d %s", w.Code, w.Body)
	}
	if w := resumeHTTP(s, session, "/api/v1/runs/run/nodes/job/release", `{}`); w.Code != 404 {
		t.Fatalf("retired HTTP %d %s", w.Code, w.Body)
	}
}
