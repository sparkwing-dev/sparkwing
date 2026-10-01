package controller_test

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestControllerFixturesKeepCredentialsAndRunsSeparate(t *testing.T) {
	firstURL, firstToken, firstStore, firstCleanup := newAuthedTestServer(t)
	defer firstCleanup()
	secondURL, secondToken, secondStore, secondCleanup := newAuthedTestServer(t)
	defer secondCleanup()

	for _, fixture := range []struct {
		url, ownToken, foreignToken string
	}{
		{firstURL, firstToken, secondToken},
		{secondURL, secondToken, firstToken},
	} {
		for _, request := range []struct {
			token string
			want  int
		}{
			{fixture.ownToken, http.StatusOK},
			{fixture.foreignToken, http.StatusUnauthorized},
		} {
			resp := getWithBearer(t, fixture.url+"/api/v1/runs", request.token)
			_ = resp.Body.Close()
			if resp.StatusCode != request.want {
				t.Fatalf("fixture credential status = %d, want %d", resp.StatusCode, request.want)
			}
		}
	}

	if err := firstStore.CreateRun(t.Context(), store.Run{
		ID: "fixture-run", Pipeline: "first", Status: "running", StartedAt: time.Unix(100, 0),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := secondStore.GetRun(t.Context(), "fixture-run"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("fresh fixture found another fixture's run: %v", err)
	}
	if err := secondStore.CreateRun(t.Context(), store.Run{
		ID: "fixture-run", Pipeline: "second", Status: "running", StartedAt: time.Unix(100, 0),
	}); err != nil {
		t.Fatal(err)
	}
	row, err := firstStore.GetRun(t.Context(), "fixture-run")
	if err != nil || row.Pipeline != "first" {
		t.Fatalf("another fixture changed the original run: %+v, %v", row, err)
	}
}
