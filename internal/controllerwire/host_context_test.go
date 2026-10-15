package controllerwire

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestHostContextLeavesRenewableLeaseLivenessToKeeper(t *testing.T) {
	parent, stopParent := context.WithCancelCause(t.Context())
	defer stopParent(nil)
	unit := ClaimedUnit{LeaseExpiresAt: time.Unix(1, 0)}
	child, stopChild := unit.HostContext(parent)
	defer stopChild()
	if _, bounded := child.Deadline(); bounded || child.Err() != nil {
		t.Fatal("renewable lease snapshot became an immutable execution deadline")
	}
	cause := errors.New("owner stopped")
	stopParent(cause)
	if !errors.Is(context.Cause(child), cause) {
		t.Fatal("host context lost the parent's cancellation cause")
	}
}
