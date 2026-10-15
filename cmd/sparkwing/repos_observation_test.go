package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/repos"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type repoObservationReader struct {
	controllerwire.RunReader
	page  controllerwire.RunPage
	err   error
	query controllerwire.RunQuery
}

func (f *repoObservationReader) ListRuns(_ context.Context, q controllerwire.RunQuery) (controllerwire.RunPage, error) {
	f.query = q
	return f.page, f.err
}

func TestRepoHistoryUsesPublicRunProjection(t *testing.T) {
	at := time.Unix(100, 0)
	reader := &repoObservationReader{page: controllerwire.RunPage{Runs: []controllerwire.RunDetail{
		{Inputs: runspec.Display{Pipeline: "deploy", Git: runspec.Git{Repository: runspec.Repository{Key: "gh:17", Display: "acme/app"}}}, StartedAt: &at},
		{Inputs: runspec.Display{Pipeline: "without-repository"}},
	}}}
	got, err := repoRunObservations(t.Context(), reader)
	want := []repos.RunObservation{{Repo: "acme/app", Pipeline: "deploy", At: at}}
	if err != nil || !reflect.DeepEqual(got, want) || reader.query.Limit != 500 {
		t.Fatalf("observations=%+v query=%+v err=%v", got, reader.query, err)
	}
	failure := errors.New("history unavailable")
	reader.err = failure
	if got, err := repoRunObservations(t.Context(), reader); !errors.Is(err, failure) || got != nil {
		t.Fatalf("history failure became absence: %+v %v", got, err)
	}
}
