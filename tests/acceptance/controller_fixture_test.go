package acceptance_test

import (
	"errors"
	"io/fs"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestControllerFixtureDistinguishesCreationAndReopening(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	now := time.Date(2000, 1, 1, 12, 0, 0, 0, time.UTC)
	var built []*store.Store
	build := func(s *store.Store) *controller.Server {
		built = append(built, s)
		return controller.New(s, nil)
	}
	s, server, err := openControllerFixture(t, path, teststore.Open, build, teststore.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if s != nil {
			if err := s.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	if server == nil || len(built) != 1 || built[0] != s || !teststore.Now(s).Equal(now) {
		t.Fatal("created store lost its controller policy binding or clock")
	}
	teststore.SeedTeam(t, s, "persisted")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = nil
	duplicate, _, err := openControllerFixture(t, path, teststore.Open, build)
	if err == nil {
		if err := duplicate.Close(); err != nil {
			t.Error(err)
		}
		t.Fatal("creating a fixture over an existing database reopened it")
	}
	if !errors.Is(err, fs.ErrExist) || len(built) != 1 {
		t.Fatalf("creation refusal = %v, policy builds %d", err, len(built))
	}
	later := now.Add(time.Hour)
	s, server, err = openControllerFixture(t, path, teststore.OpenExisting, build, teststore.WithClock(func() time.Time { return later }))
	if err != nil {
		t.Fatal(err)
	}
	if server == nil || len(built) != 2 || built[1] != s || !teststore.Now(s).Equal(later) {
		t.Fatal("reopened store lost its controller policy binding or clock")
	}
	var team string
	if err := teststore.SQL(t, s).QueryRowContext(t.Context(), "SELECT name FROM teams WHERE name='persisted'").Scan(&team); err != nil || team != "persisted" {
		t.Fatalf("reopened fixture lost persisted data: team=%q err=%v", team, err)
	}
}
