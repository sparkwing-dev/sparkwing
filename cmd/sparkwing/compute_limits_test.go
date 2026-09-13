package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestRenderComputeLimits(t *testing.T) {
	t.Parallel()
	var view computeLimitsResp
	view.Limits = map[string]int64{
		store.ComputeLimitConcurrentRunners: 4,
		store.ComputeLimitGlobalRunners:     50,
	}
	view.Usage.Runners = 4
	view.Usage.ByPrincipal = map[string]int64{"agent:cloud": 4}
	view.Usage.AlarmReached = true

	var buf bytes.Buffer
	if err := renderComputeLimits(&buf, view); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"MAX_CONCURRENT_RUNNERS", "4", "MAX_GLOBAL_RUNNERS", "50",
		"MAX_NODES_PER_RUN", "unlimited", "CLOUD RUNNERS", "agent:cloud", "ALARM",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("compute limits output is missing %q:\n%s", want, out)
		}
	}
	for _, name := range store.ComputeLimitNames() {
		if !strings.Contains(out, strings.ToUpper(name)) {
			t.Errorf("output omits the guard %q:\n%s", name, out)
		}
	}
}
