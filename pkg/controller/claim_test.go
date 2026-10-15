package controller_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestClaim_FIFOOrdering(t *testing.T) {
	dir := t.TempDir()
	st, err := teststore.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	for i, name := range []string{"first", "second", "third"} {
		if err := st.CreateTrigger(context.Background(), store.Trigger{
			ID:        "trig-" + name,
			Pipeline:  name,
			CreatedAt: time.Now().Add(time.Duration(i) * time.Millisecond),
		}); err != nil {
			t.Fatalf("CreateTrigger %s: %v", name, err)
		}
	}

	for _, want := range []string{"first", "second", "third"} {
		got, err := st.ClaimNextTrigger(context.Background(), 0)
		if err != nil {
			t.Fatalf("ClaimNextTrigger for %s: %v", want, err)
		}
		if got.Pipeline != want {
			t.Errorf("claimed %q, want %q", got.Pipeline, want)
		}
	}

	_, err = st.ClaimNextTrigger(context.Background(), 0)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("4th claim err=%v want ErrNotFound", err)
	}
}
