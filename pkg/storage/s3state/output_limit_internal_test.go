package s3state

import (
	"context"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestTakeRunOutputRoom_HoldsARunToItsLimit(t *testing.T) {
	art, err := fs.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b := New(art)
	t.Cleanup(func() { _ = b.Close() })
	ctx := context.Background()
	for i := range store.MaxRunOutputBytes / store.MaxOutputBytes {
		if err := b.takeRunOutputRoom(ctx, "run-a", store.MaxOutputBytes); err != nil {
			t.Fatalf("output %d within the run's 1 GiB: %v", i, err)
		}
	}
	if err := b.takeRunOutputRoom(ctx, "run-a", 1); !errors.Is(err, store.ErrOutputLimit) {
		t.Fatalf("a byte past the run's 1 GiB: err = %v, want the run limit", err)
	}
	if err := b.takeRunOutputRoom(ctx, "run-b", store.MaxOutputBytes); err != nil {
		t.Fatalf("another run is charged for the first run's outputs: %v", err)
	}
}
