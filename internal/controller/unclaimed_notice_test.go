package controller

import (
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestAssertedPoolWaitingNoticeExplainsVerifiedDefaultRemedy(t *testing.T) {
	now := time.Now()
	view := store.RunView{Snapshot: store.RunSnapshot{StoreNow: now}, Waiting: &store.UnclaimedUnit{Kind: capability.UnitPlan, Since: now.Add(-time.Minute)}}
	provenance, err := runspec.AssertedProvenance("account/owner", "refs/heads/main", "0123456789abcdef0123456789abcdef01234567", "ci")
	if err != nil {
		t.Fatal(err)
	}
	view.Snapshot.Run.Request = runspec.SealedRequest{Request: runspec.Request{Dispatch: runspec.DispatchController, Options: runspec.Options{Requires: runspec.Setting[[]string]{Optional: runspec.Optional[[]string]{Set: true, Value: []string{"pool=deploy"}}}}}, Provenance: provenance}
	notice := unclaimedNotice(view)
	if notice == nil {
		t.Fatal("missing waiting notice")
	}
	for _, want := range []string{"reserved", "verified default-branch work", "push", "default branch", "GitHub App", "pool without reserved secrets"} {
		if !strings.Contains(notice.Reason, want) {
			t.Errorf("notice = %q, want %q", notice.Reason, want)
		}
	}
}
