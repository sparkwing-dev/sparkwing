package sparkwingtest_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
)

type preparationInputs struct {
	Required string   `flag:"required" required:"true"`
	Mode     string   `flag:"mode" default:"ready" enum:"ready,other"`
	Items    []string `flag:"items"`
}
type preparationPipeline struct{}

func (preparationPipeline) Plan(_ context.Context, plan *sparkwing.Plan, in preparationInputs, _ sparkwing.RunContext) error {
	name := "unprepared"
	if in.Required == "supplied" && in.Mode == "ready" && reflect.DeepEqual(in.Items, []string{"one", "two"}) {
		name = "prepared"
	}
	sparkwing.Job(plan, name, func(context.Context) error { return nil })
	return nil
}
func init() {
	sparkwing.Register[preparationInputs]("sparkwingtest-preparation", func() sparkwing.Pipeline[preparationInputs] { return preparationPipeline{} })
}

func TestHostPreparesArguments(t *testing.T) {
	host := sparkwingtest.New(sparkwingtest.Options{})
	t.Cleanup(func() { _ = host.Close() })
	if plan, err := host.Plan(t.Context(), "sparkwingtest-preparation", map[string]string{"required": "supplied", "items": " one, , two "}); err != nil || len(plan.Jobs) != 1 || plan.Jobs[0].ID != "prepared" {
		t.Fatalf("plan = %+v, error = %v", plan, err)
	}
	for _, tc := range []struct {
		args map[string]string
		want string
	}{
		{map[string]string{}, "argument --required is required"},
		{map[string]string{"required": "supplied", "mode": "invalid"}, "argument --mode is not an allowed value"},
		{map[string]string{"required": "supplied", "unknown": "value"}, "no flag --unknown is declared"},
	} {
		if _, err := host.Plan(t.Context(), "sparkwingtest-preparation", tc.args); err == nil || err.Error() != tc.want {
			t.Errorf("Plan(%v) error = %v, want %s", tc.args, err, tc.want)
		}
	}
}

func TestHostRefusesInvalidAnnotations(t *testing.T) {
	for _, frame := range []nodeproto.FrameLog{
		{Event: "annotation", Level: "info", Attrs: []byte(`{"line":5,"end_line":4}`)},
		{Event: "annotation", Level: "info", Attrs: []byte(`{"line":-1}`)},
		{Event: "annotation", Level: "warning"},
	} {
		t.Run(string(frame.Attrs)+frame.Level, func(t *testing.T) {
			host := sparkwingtest.New(sparkwingtest.Options{})
			defer host.Close()
			defer func() {
				if recover() == nil {
					t.Error("invalid annotation accepted")
				}
				if len(host.Records()) != 0 {
					t.Error("invalid annotation persisted")
				}
			}()
			sdkserve.ServicesFrom(host.Context(t.Context())).Emit(frame)
		})
	}
}
