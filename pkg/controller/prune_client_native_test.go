package controller

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func TestRunPrunerAcknowledgesCommittedDeletion(t *testing.T) {
	for _, selector := range []string{"run", "1"} {
		t.Run(selector, func(t *testing.T) {
			server, session := pauseHTTPFixture(t)
			options, _ := selectorClientOptions(t, server, session, `{"grants":["runs.read","runs.control"]}`)
			engineSQL(t, server.store, `UPDATE runs SET status='success',finished_at=1 WHERE team='a' AND id='run'`)
			engineSQL(t, server.store, `INSERT INTO runs(team,id,number,pipeline,status,finished_at,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days) SELECT team,'run-1',2,pipeline,status,finished_at,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days FROM runs WHERE team='a' AND id='run'`)
			pruner, err := client.NewRunPruner(options)
			if err != nil {
				t.Fatal(err)
			}
			deleteErr := pruner.DeleteRun(t.Context(), selector)
			var count int
			if err := server.store.DB().QueryRow(`SELECT COUNT(*) FROM runs WHERE team='a' AND id='run'`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("run remains after deletion: count=%d %v", count, err)
			}
			if deleteErr != nil {
				t.Fatalf("client rejected committed deletion: %v", deleteErr)
			}
			if err := server.store.DB().QueryRow(`SELECT COUNT(*) FROM runs WHERE team='a' AND id='run-1'`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("numeric selector changed a different stable ID: count=%d %v", count, err)
			}
		})
	}
}
