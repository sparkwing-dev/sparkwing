package store

import (
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

var planIndexName = regexp.MustCompile(`\b(idx_\w+|sqlite_autoindex_\w+)\b`)

func planIndexNames(plan string) []string {
	var names []string
	for line := range strings.SplitSeq(plan, "\n") {
		names = append(names, planIndexName.FindString(line))
	}
	return names
}

func TestExpiredApprovalSearchStartsFromUnresolvedApprovals(t *testing.T) {
	s := newTestStore(t)
	conn, err := s.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	plan := explainLines(t, conn, `EXPLAIN QUERY PLAN `+expiredApprovalsQuery, time.Now().UnixNano())
	if got, want := planIndexNames(plan), []string{"idx_approvals_pending", "sqlite_autoindex_nodes_1"}; !slices.Equal(got, want) {
		t.Fatalf("approval search must read idx_approvals_pending and then each node by key, not scan nodes; indexes %q, plan:\n%s", got, plan)
	}
}
