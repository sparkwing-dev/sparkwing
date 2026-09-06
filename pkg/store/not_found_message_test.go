package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestAMissingLookupNamesWhatItLookedFor(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	lookups := map[string]struct {
		call func() error
		want []string
	}{
		"GetRun":        {func() error { _, err := s.GetRun(ctx, "run-absent"); return err }, []string{"run", "run-absent"}},
		"GetTrigger":    {func() error { _, err := s.GetTrigger(ctx, "trig-absent"); return err }, []string{"trigger", "trig-absent"}},
		"RequestCancel": {func() error { return s.RequestCancel(ctx, "cancel-absent") }, []string{"trigger", "cancel-absent"}},
		"GetSecretForRun": {
			func() error { _, err := s.GetSecretForRun("secret-absent", ""); return err },
			[]string{"secret", "secret-absent"},
		},
	}

	for name, lookup := range lookups {
		t.Run(name, func(t *testing.T) {
			err := lookup.call()
			if err == nil {
				t.Fatal("a lookup that matched nothing returned no error")
			}
			if !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("errors.Is(err, ErrNotFound) is false for %v; every caller branches on that", err)
			}
			for _, want := range lookup.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not name %q, so an operator reading a log line cannot "+
						"tell which lookup missed", err, want)
				}
			}
		})
	}
}
