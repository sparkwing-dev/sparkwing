package store

import (
	"strings"
	"testing"
)

func TestRunListFindsRetriesThroughRetryIndex(t *testing.T) {
	s := newTestStore(t)
	conn, err := s.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	query := `EXPLAIN QUERY PLAN SELECT ` + runColumns + ` FROM runs WHERE team = ? ORDER BY COALESCE(started_at,0) DESC, id DESC LIMIT ?`
	plan := explainLines(t, conn, query, "default", 51)
	var retryLines []string
	for line := range strings.SplitSeq(plan, "\n") {
		if strings.Contains(line, " retry ") || strings.HasSuffix(line, " retry") {
			retryLines = append(retryLines, line)
		}
	}
	if len(retryLines) != 1 || !strings.Contains(retryLines[0], "idx_runs_retry_of (team=? AND retry_of=?)") {
		t.Fatalf("RetriedAs subquery must search idx_runs_retry_of, not walk the team's runs; plan:\n%s", plan)
	}
}
