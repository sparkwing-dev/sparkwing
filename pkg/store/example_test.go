package store_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func ExampleOpen() {
	dir, _ := os.MkdirTemp("", "sparkwing-store-")
	defer os.RemoveAll(dir)

	s, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		fmt.Println("open:", err)
		return
	}

	ctx := context.Background()
	if err := s.CreateRun(ctx, store.Run{
		ID:        "run-1",
		Pipeline:  "build",
		Status:    "running",
		StartedAt: time.Now(),
	}); err != nil {
		fmt.Println("create:", err)
		return
	}

	r, err := s.GetRun(ctx, "run-1")
	if err != nil {
		fmt.Println("get:", err)
		return
	}
	fmt.Printf("pipeline=%s status=%s\n", r.Pipeline, r.Status)

	if err := s.FinishRun(ctx, "run-1", "success", ""); err != nil {
		fmt.Println("finish:", err)
		return
	}
	r, _ = s.GetRun(ctx, "run-1")
	fmt.Printf("final status=%s\n", r.Status)
	// Output:
	// pipeline=build status=running
	// final status=success
}
