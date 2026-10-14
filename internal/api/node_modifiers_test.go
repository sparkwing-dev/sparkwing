package api_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/api"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

// NodeModifiers is decoded from the plan snapshot, so a PlanModifiers field it
// lacks never reaches the dashboard.
func TestNodeModifiers_CoverEveryPlanModifier(t *testing.T) {
	have := map[string]bool{}
	for _, f := range reflect.VisibleFields(reflect.TypeFor[api.NodeModifiers]()) {
		have[strings.Split(f.Tag.Get("json"), ",")[0]] = true
	}
	for _, f := range reflect.VisibleFields(reflect.TypeFor[nodeproto.PlanModifiers]()) {
		if tag := strings.Split(f.Tag.Get("json"), ",")[0]; !have[tag] {
			t.Errorf("NodeModifiers has no %q, so the dashboard drops it", tag)
		}
	}
}
