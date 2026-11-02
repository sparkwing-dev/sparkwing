package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestRetryTreeReadsInOneStatementAtAnySize(t *testing.T) {
	for _, size := range []int{3, 300} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "s.db")
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			tn := s.defaultTenant()
			parent := ""
			for i := range size {
				id := fmt.Sprintf("run-%03d", i)
				run := Run{RunSummary: runstate.RunSummary{ID: id, Pipeline: "ci", Status: "success", CreatedAt: time.Unix(int64(i+1), 0), RetryOf: parent}}
				if err := tn.TestOnlySeedRun(t.Context(), run); err != nil {
					t.Fatal(err)
				}
				parent = id
			}
			trace := &claimBenchmarkTrace{enabled: true, allStatements: true}
			probe := fmt.Sprintf("retry_tree_statements_%d_%x", size, time.Now().UnixNano())
			sql.Register(probe, &claimBenchmarkDriver{Driver: s.db.Driver(), trace: trace})
			counted, err := sql.Open(probe, sqliteDSN(path))
			if err != nil {
				t.Fatal(err)
			}
			original := s.db
			s.db = counted
			t.Cleanup(func() {
				s.db = original
				_ = counted.Close()
			})
			members, err := tn.ListRunRetryTree(t.Context(), "run-001")
			if err != nil {
				t.Fatal(err)
			}
			if len(members) != size || len(trace.statements) != 1 {
				t.Fatalf("tree of %d runs read %d members in %d statements, want one statement", size, len(members), len(trace.statements))
			}
		})
	}
}
