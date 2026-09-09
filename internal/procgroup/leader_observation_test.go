package procgroup

import (
	"syscall"
	"testing"
)

func TestLeaderExitRequiresSuccessfulObservation(t *testing.T) {
	for _, test := range []struct {
		name           string
		complete       bool
		observationErr error
		wantExited     bool
	}{
		{name: "pending"},
		{name: "exited", complete: true, wantExited: true},
		{name: "observer failure", complete: true, observationErr: syscall.EIO},
	} {
		t.Run(test.name, func(t *testing.T) {
			group := &Group{leaderDone: make(chan struct{}), leaderErr: test.observationErr}
			if test.complete {
				close(group.leaderDone)
			}
			if exited := group.leaderHasExited(); exited != test.wantExited {
				t.Fatalf("leader exited = %v, want %v", exited, test.wantExited)
			}
		})
	}
}
