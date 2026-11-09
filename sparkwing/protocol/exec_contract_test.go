package nodeproto

import (
	"errors"
	"testing"
)

func TestExecFramesRequireAnAbsoluteDirectory(t *testing.T) {
	for _, line := range []string{
		`{"exec":"1","argv":["true"],"dir":"relative","env":[]}`,
		`{"env":[],"exec":"1","argv":["true"],"dir":"relative"}`,
	} {
		if _, err := ParseFrame([]byte(line)); !errors.Is(err, ErrMalformed) {
			t.Fatalf("%s parsed with %v", line, err)
		}
	}
	frame, err := ParseFrame([]byte(`{"exec":"1","argv":["true"],"dir":"/","env":[]}`))
	if err != nil || frame.Exec == nil || frame.Exec.Dir != "/" {
		t.Fatalf("absolute dir: %#v, %v", frame, err)
	}
}
