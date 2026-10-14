package store

import (
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

// The plan decode is strict, so a PlanModifiers field this struct lacks makes
// the launcher refuse every plan that sets it.
func TestSubmittedModifiers_CoverEveryPlanModifier(t *testing.T) {
	have := map[string]bool{}
	for _, f := range reflect.VisibleFields(reflect.TypeFor[submittedModifiers]()) {
		have[strings.Split(f.Tag.Get("json"), ",")[0]] = true
	}
	for _, f := range reflect.VisibleFields(reflect.TypeFor[nodeproto.PlanModifiers]()) {
		if tag := strings.Split(f.Tag.Get("json"), ",")[0]; !have[tag] {
			t.Errorf("submittedModifiers has no %q, so AcceptPlan refuses a plan that sets it", tag)
		}
	}
}
