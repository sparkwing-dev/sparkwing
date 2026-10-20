package controller

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestApprovalTimeoutPolicy_SDKMatchesStoreVocabulary(t *testing.T) {
	pairs := map[string]string{
		string(sparkwing.ApprovalFail):    store.ApprovalOnTimeoutFail,
		string(sparkwing.ApprovalDeny):    store.ApprovalOnTimeoutDeny,
		string(sparkwing.ApprovalApprove): store.ApprovalOnTimeoutApprove,
	}
	for got, want := range pairs {
		if got != want {
			t.Errorf("sparkwing approval policy %q drifted from store vocabulary %q", got, want)
		}
	}
}
