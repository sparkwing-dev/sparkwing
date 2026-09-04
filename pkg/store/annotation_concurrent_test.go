package store_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const (
	annotators      = 6
	annotationsEach = 5
	annotationsWant = annotators * annotationsEach
)

func seedAnnotationTarget(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "build", Status: "running"}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
}

func annotateConcurrently(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < annotators; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for j := 0; j < annotationsEach; j++ {
				msg := fmt.Sprintf("worker %d note %d", worker, j)
				if err := st.AppendNodeAnnotation(ctx, "run-1", "build", msg); err != nil {
					t.Errorf("AppendNodeAnnotation(%s): %v", msg, err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
}

func checkAnnotationsKept(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	node, err := st.GetNode(ctx, "run-1", "build")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if len(node.Annotations) != annotationsWant {
		t.Errorf("node annotations = %d, want %d: an appender's entry was overwritten",
			len(node.Annotations), annotationsWant)
	}
	run, err := st.GetRun(ctx, "run-1")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if len(run.Annotations) != annotationsWant {
		t.Errorf("run annotations = %d, want %d", len(run.Annotations), annotationsWant)
	}
	if run.AnnotationCount != len(node.Annotations) {
		t.Errorf("run annotation_count = %d but %d annotations are stored; the counter is "+
			"an atomic increment, so it counts appends the list lost",
			run.AnnotationCount, len(node.Annotations))
	}
}

// TestPostgresAppendNodeAnnotationKeepsEveryEntry pins what the read-modify-write
// promises. Under Postgres' READ COMMITTED the transaction alone does not
// deliver it: two appenders read the same list and the second UPDATE writes a
// value computed before the first one landed, while the annotation counter,
// being a real atomic increment, counts both.
func TestPostgresAppendNodeAnnotationKeepsEveryEntry(t *testing.T) {
	st := openPGTestStore(t)
	seedAnnotationTarget(t, st)
	annotateConcurrently(t, st)
	checkAnnotationsKept(t, st)
}

// TestAppendNodeAnnotationKeepsEveryEntry is the SQLite half, where immediate
// transactions already serialized the read and the write.
func TestAppendNodeAnnotationKeepsEveryEntry(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "annotations.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	seedAnnotationTarget(t, st)
	annotateConcurrently(t, st)
	checkAnnotationsKept(t, st)
}

// TestPostgresAppendStepAnnotationKeepsEveryEntry covers the step-scoped
// append, which reads and rewrites node_steps the same way.
func TestPostgresAppendStepAnnotationKeepsEveryEntry(t *testing.T) {
	st := openPGTestStore(t)
	seedAnnotationTarget(t, st)
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < annotators; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for j := 0; j < annotationsEach; j++ {
				msg := fmt.Sprintf("worker %d note %d", worker, j)
				if err := st.AppendStepAnnotation(ctx, "run-1", "build", "step-1", msg); err != nil {
					t.Errorf("AppendStepAnnotation(%s): %v", msg, err)
					return
				}
			}
		}(i)
	}
	wg.Wait()

	steps, err := st.ListNodeSteps(ctx, "run-1")
	if err != nil {
		t.Fatalf("ListNodeSteps: %v", err)
	}
	if len(steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(steps))
	}
	if len(steps[0].Annotations) != annotationsWant {
		t.Errorf("step annotations = %d, want %d", len(steps[0].Annotations), annotationsWant)
	}
}
