package crons

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

const pushedRepoURL = "https://github.com/acme/widgets.git"

type pushedFixture struct {
	*Service
	Store *store.Store
}

func pushedService(t *testing.T) *pushedFixture {
	t.Helper()
	st, err := teststore.OpenExisting(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &pushedFixture{Service: &Service{Operator: st.AsOperator(), Schedules: teststore.ForTeam(t, st, store.DefaultTeam), ArmedBy: "operator@laptop"}, Store: st}
}

func TestControllerHealthReportsTimerLoop(t *testing.T) {
	svc := pushedService(t)
	health, err := svc.ControllerHealth(context.Background())
	if err != nil || health.Timer.Detail != controllerTimerDetail {
		t.Fatalf("health: %+v %v", health, err)
	}
}

func TestPushedRepoRecognizesCloneURL(t *testing.T) {
	if !pushedRepo(pushedRepoURL) || pushedRepo("/tmp/checkout") {
		t.Fatal("clone URL classification")
	}
}
