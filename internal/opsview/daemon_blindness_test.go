package opsview

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestLocalQueue_DistinguishesServingAbsentAndUnreachable(t *testing.T) {
	blocked := errors.New("dial unix /tmp/sparkwing-502-abc/d.sock: connect: operation not permitted")
	for _, format := range []string{"json", "plain", "pretty"} {
		var unreachable, absent, idle bytes.Buffer
		if err := renderUnreachableDaemon(&unreachable, format, blocked); err != nil {
			t.Fatalf("%s unreachable: %v", format, err)
		}
		if err := RenderNoDaemon(&absent, format); err != nil {
			t.Fatalf("%s absent: %v", format, err)
		}
		if err := renderLocalQueue(&idle, wingwire.QueueState{}, serving(), format); err != nil {
			t.Fatalf("%s idle: %v", format, err)
		}
		if unreachable.String() == absent.String() {
			t.Errorf("%s: unreachable renders the same as absent:\n%s", format, unreachable.String())
		}
		if unreachable.String() == idle.String() {
			t.Errorf("%s: unreachable renders the same as an idle daemon:\n%s", format, unreachable.String())
		}
		if absent.String() == idle.String() {
			t.Errorf("%s: absent renders the same as an idle daemon:\n%s", format, absent.String())
		}
		if format == "json" && (!strings.Contains(absent.String(), `"reachable":false`) || !strings.Contains(idle.String(), `"reachable":true`)) {
			t.Errorf("json hides daemon reachability: absent=%s idle=%s", absent.String(), idle.String())
		}
	}

	var pretty bytes.Buffer
	if err := renderUnreachableDaemon(&pretty, "pretty", blocked); err != nil {
		t.Fatalf("pretty: %v", err)
	}
	if !strings.Contains(pretty.String(), "operation not permitted") {
		t.Errorf("unreachable view buries the dial cause instead of leading with it:\n%s", pretty.String())
	}
}
