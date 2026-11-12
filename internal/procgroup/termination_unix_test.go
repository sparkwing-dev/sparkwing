//go:build !windows

package procgroup

import (
	"errors"
	"os/exec"
	"syscall"
	"testing"
)

func TestTerminationMatchesOnlyItsOwnSignal(t *testing.T) {
	for _, test := range []struct {
		name   string
		signal syscall.Signal
		report Termination
		want   bool
	}{
		{"owned term", syscall.SIGTERM, Termination{term: true}, true},
		{"external kill after owned term", syscall.SIGKILL, Termination{term: true}, false},
		{"owned escalation", syscall.SIGKILL, Termination{term: true, kill: true}, true},
		{"external term", syscall.SIGTERM, Termination{}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			name := "TERM"
			if test.signal == syscall.SIGKILL {
				name = "KILL"
			}
			err := exec.Command("/bin/sh", "-c", "kill -"+name+" $$").Run()
			if err == nil || test.report.Caused(err) != test.want {
				t.Fatalf("signal attribution=%v error=%v", test.report.Caused(err), err)
			}
			if test.report.Caused(errors.Join(err, errors.New("cleanup failed"))) {
				t.Fatal("joined cleanup failure hidden")
			}
		})
	}
}
