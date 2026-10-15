package nodehost

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func TestOnlyRefusesWhatTheDispatcherRefused(t *testing.T) {
	doc := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "build", Steps: []nodeproto.StepSpec{{ID: "body"}}}, {ID: "test", Steps: []nodeproto.StepSpec{{ID: "body"}}}}}
	for _, tc := range []struct{ only, start, want string }{
		{"deploy", "", `--only="deploy" matched no jobs (declared: [build test])`},
		{"[", "", `--only="["`},
		{"build", "compile", "--only is mutually exclusive with --start-at / --stop-at"},
	} {
		if _, err := onlySkips(doc, tc.only, tc.start, ""); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("only=%q start=%q: err=%v, want %q", tc.only, tc.start, err, tc.want)
		}
	}
}
