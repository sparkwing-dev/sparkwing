package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func FuzzDebugResumeClientRequiresCurrentAuthority(f *testing.F) {
	for state := range byte(5) {
		f.Add(state, "other-run", "other-job")
	}
	f.Fuzz(func(t *testing.T, state byte, run, job string) {
		if len(run) > 512 || len(job) > 512 {
			t.Skip()
		}
		s, session := pauseHTTPFixture(t)
		grants := `{"grants":["runs.read","runs.control"]}`
		if state%5 == 3 {
			grants = `{"grants":["runs.read"]}`
		}
		w := credentialRequestHTTP(t, s.Handler(), session, grants, false)
		var issued credentialResponse
		if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &issued) != nil {
			t.Fatalf("issue authority: HTTP %d %s", w.Code, w.Body)
		}
		control, err := client.NewDebugControl(client.EngineOptions{URL: "https://controller.test", HTTP: &http.Client{Transport: localHostRoundTripper{s.Handler()}}, Bearer: func(context.Context) (string, error) { return issued.Capability, nil }})
		if err != nil {
			t.Fatal(err)
		}
		observed, err := control.ReadDebugPause(t.Context(), "run", "job")
		if err != nil || observed.Pause == nil || observed.RunID != "run" || observed.JobID != "job" {
			t.Fatalf("observe selected pause: %+v %v", observed, err)
		}
		switch state % 5 {
		case 1:
			engineSQL(t, s.store, `UPDATE capabilities SET revoked_at=1 WHERE prefix=?`, issued.Prefix)
		case 2:
			engineSQL(t, s.store, `DELETE FROM memberships WHERE team='a' AND account_id='member'`)
		case 4:
			runDigest, jobDigest := sha256.Sum256([]byte(run)), sha256.Sum256([]byte(job))
			observed.RunID, observed.JobID = hex.EncodeToString(runDigest[:]), hex.EncodeToString(jobDigest[:])
			engineSQL(t, s.store, `INSERT INTO teams(name,created_at,updated_at) VALUES ('b',1,1)`)
			engineSQL(t, s.store, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days) SELECT 'b',?,1,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days FROM runs WHERE team='a' AND id='run'`, observed.RunID)
			engineSQL(t, s.store, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json) VALUES ('b',?,?,'pending',?,?)`, observed.RunID, observed.JobID, []byte(`{}`), []byte(`{}`))
			engineSQL(t, s.store, `INSERT INTO debug_pauses(team,run_id,node_id,reason,paused_at,expires_at) SELECT 'b',?,?,reason,paused_at,expires_at FROM debug_pauses WHERE team='a' AND run_id='run' AND node_id='job'`, observed.RunID, observed.JobID)
		}
		err = control.ResumeDebugPause(t.Context(), observed.RunID, observed.JobID)
		if (err == nil) != (state%5 == 0) {
			t.Fatalf("authority state %d: %v", state%5, err)
		}
		if err == nil {
			if err := control.ResumeDebugPause(t.Context(), observed.RunID, observed.JobID); err != nil {
				t.Fatalf("repeat release: %v", err)
			}
		}
		tenant, err := s.store.ForTeam(t.Context(), "a")
		if err != nil {
			t.Fatal(err)
		}
		pauses, err := tenant.ListDebugPauses(t.Context(), "run")
		if err != nil || len(pauses) != 1 || (pauses[0].ReleasedAt != nil) != (state%5 == 0) {
			t.Fatalf("committed pauses: %+v %v", pauses, err)
		}
		if state%5 == 0 && (pauses[0].ReleasedBy != "member" || pauses[0].ReleaseKind != store.PauseReleaseManual) {
			t.Fatalf("release attribution: %+v", pauses[0])
		}
		var events int
		wantEvents := 0
		if state%5 == 0 {
			wantEvents = 1
		}
		if err := teststore.SQL(t, s.store).QueryRow(`SELECT COUNT(*) FROM events WHERE kind='debug.resumed'`).Scan(&events); err != nil || events != wantEvents {
			t.Fatalf("release events: %d %v", events, err)
		}
		if state%5 == 4 {
			var released int
			if err := teststore.SQL(t, s.store).QueryRow(`SELECT COUNT(*) FROM debug_pauses WHERE team='b' AND released_at IS NOT NULL`).Scan(&released); err != nil || released != 0 {
				t.Fatalf("foreign releases: %d %v", released, err)
			}
		}
	})
}
