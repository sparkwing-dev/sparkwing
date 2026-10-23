//go:build !windows

package procgroup

import "testing"

func TestDescendantScansIgnoreTerminatedProcesses(t *testing.T) {
	for _, session := range []bool{false, true} {
		for _, state := range []string{"Z", "X", "x", "S"} {
			processes := []Info{{PID: 10, Group: 10, Session: 10, State: "S"}, {PID: 11, Group: 10, Session: 10, State: state}}
			want := state != "S"
			if got := descendantsEmptyInTable(processes, 10, session); got != want {
				t.Errorf("session=%v state=%s: empty=%v want%v", session, state, got, want)
			}
		}
	}
}
