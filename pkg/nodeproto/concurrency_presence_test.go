package nodeproto

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

func TestConcurrencyPreservesOmittedOptions(t *testing.T) {
	plan := checkPlan(t, `{"jobs":[],"concurrency":[{"group":"gate"}]}`, planContext(), "")
	spec := plan.Concurrency[0]
	if spec.Scope != nil || spec.OnLimit != nil || spec.Capacity != nil || spec.Cost != nil {
		t.Fatalf("options defaulted in declaration: %+v", spec)
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"group":"gate"}` {
		t.Fatalf("declaration round trip: %s", raw)
	}
	for _, option := range []string{`"scope":""`, `"on_limit":""`, `"capacity":0`, `"cost":0`, `"scope":null`, `"capacity":null`, `"cost":null`, `"on_limit":null`} {
		t.Run(option, func(t *testing.T) {
			checkPlan(t, fmt.Sprintf(`{"jobs":[],"concurrency":[{"group":"gate",%s}]}`, option), planContext(), apierr.PlanInvalid)
		})
	}
}

func TestCancelSelectorsAreExclusive(t *testing.T) {
	for _, selector := range []string{`"job":"j"`, `"job":"j","step":"s"`, `"target":"eval-request"`} {
		if _, err := ParseRequest([]byte(`{"id":"cancel","op":"cancel",` + selector + `}`)); err != nil {
			t.Fatalf("selector %s: %v", selector, err)
		}
	}
	for _, selector := range []string{`"job":""`, `"step":"s"`, `"target":""`, `"target":null`, `"target":"eval-request","job":"j"`, `"target":"eval-request","step":"s"`, `"target":"eval-request","job":""`, `"target":"","job":"j"`, `"job":"j","step":""`} {
		if _, err := ParseRequest([]byte(`{"id":"cancel","op":"cancel",` + selector + `}`)); err == nil {
			t.Fatalf("invalid selector accepted: %s", selector)
		}
	}
	if _, err := ParseRequest([]byte(`{"id":"describe","op":"describe","target":"eval-request"}`)); err == nil {
		t.Fatal("target accepted outside cancellation")
	}
}
