package nodehost

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

func TestPlanningLogsFilterDebugBeforeBufferingAndAfterRelease(t *testing.T) {
	for _, debug := range []bool{false, true} {
		t.Run(fmt.Sprint(debug), func(t *testing.T) {
			var levels []string
			logs := planningLogs{debug: debug, masker: secrets.NewMasker(), sink: func(_ context.Context, record nodeproto.FrameLog) error {
				levels = append(levels, record.Level)
				return nil
			}}
			for _, level := range []string{"debug", "info"} {
				if err := logs.write(t.Context(), nodeproto.FrameLog{Level: level, Message: "before"}); err != nil {
					t.Fatal(err)
				}
			}
			want := []string{"info"}
			if debug {
				want = []string{"debug", "info"}
			}
			if len(logs.buffer) != len(want) || len(levels) != 0 {
				t.Fatalf("buffer=%v emitted=%v", logs.buffer, levels)
			}
			if err := logs.release(t.Context()); err != nil {
				t.Fatal(err)
			}
			for _, level := range []string{"debug", "info"} {
				if err := logs.write(t.Context(), nodeproto.FrameLog{Level: level, Message: "after"}); err != nil {
					t.Fatal(err)
				}
			}
			want = append(want, slices.Clone(want)...)
			if !slices.Equal(levels, want) {
				t.Fatalf("levels=%v want=%v", levels, want)
			}
		})
	}
}

func TestDisabledDebugDoesNotConsumePlanningBuffer(t *testing.T) {
	var levels []string
	logs := planningLogs{masker: secrets.NewMasker(), sink: func(_ context.Context, record nodeproto.FrameLog) error {
		levels = append(levels, record.Level)
		return nil
	}}
	if err := logs.write(t.Context(), nodeproto.FrameLog{Level: "debug", Message: strings.Repeat("x", planningLogBufferBytes)}); err != nil {
		t.Fatal(err)
	}
	if logs.bytes != 0 || logs.dropped != 0 {
		t.Fatal("disabled debug consumed buffer capacity")
	}
	if err := logs.write(t.Context(), nodeproto.FrameLog{Level: "info", Message: "visible"}); err != nil {
		t.Fatal(err)
	}
	if err := logs.release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(levels, []string{"info"}) {
		t.Fatalf("levels=%v", levels)
	}
}

func TestNativeLogIngressUsesAcceptedDebugPolicy(t *testing.T) {
	for _, debug := range []bool{false, true} {
		t.Run(fmt.Sprint(debug), func(t *testing.T) {
			processes, lookup, delegate, services := nativeProcessesFixture(t)
			grant, err := processes.Delegate(t.Context(), lookup, delegate, services)
			if err != nil {
				t.Fatal(err)
			}
			input, _, _, _ := executorFixture(t)
			var spec runspec.Spec
			if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
				t.Fatal(err)
			}
			spec.Resolved.Settings.Options.Debug.Value = debug
			input.Accepted.Spec, err = json.Marshal(spec)
			if err != nil {
				t.Fatal(err)
			}
			var levels []string
			processes.Session.Debug = !debug
			processes.Log = func(_ context.Context, _, _ string, record nodeproto.FrameLog) error {
				levels = append(levels, record.Level)
				return nil
			}
			config, err := processes.configuration(t.Context(), grant.Binding, grant.Revoke, ExecutorActivity{}, input.Accepted)
			if err != nil {
				t.Fatal(err)
			}
			for _, level := range []string{"debug", "info"} {
				if err := config.Log(t.Context(), nodeproto.FrameLog{Level: level}); err != nil {
					t.Fatal(err)
				}
			}
			want := []string{"info"}
			if debug {
				want = []string{"debug", "info"}
			}
			if !slices.Equal(levels, want) {
				t.Fatalf("levels=%v want=%v", levels, want)
			}
		})
	}
}
