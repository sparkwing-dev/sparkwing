package nodehost

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type usageStore struct {
	usage  []store.NodeUsage
	kinds  []store.MetricKind
	refuse store.MetricKind
}

func (s *usageStore) AddNodeUsage(_ context.Context, _, _ string, u store.NodeUsage) error {
	s.usage = append(s.usage, u)
	return nil
}

func (s *usageStore) AddNodeMetricSample(_ context.Context, _, _ string, sample store.MetricSample) error {
	if sample.Kind == s.refuse {
		return errors.New("kind not supported")
	}
	s.kinds = append(s.kinds, sample.Kind)
	return nil
}

func TestRecordUsage(t *testing.T) {
	if RecordUsage(nil) != nil {
		t.Fatal("nil store must give a nil hook")
	}
	n := &NodeRun{Run: &Run{ID: "r1", Logger: slog.Default(), validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}}, Plan: nodeproto.JobSpec{ID: "build", Steps: []nodeproto.StepSpec{{ID: "body"}}}}
	full := Usage{CPUTime: time.Second, MaxRSSBytes: 1 << 20, Wall: 2 * time.Second, SessionWide: true}
	leaderOnly := full
	leaderOnly.SessionWide = false

	cases := []struct {
		name      string
		refuse    store.MetricKind
		usage     Usage
		wantUsage int
		wantKinds []store.MetricKind
	}{
		{name: "session-wide records figures only", usage: full, wantUsage: 1},
		{name: "leader-only is marked partial", usage: leaderOnly, wantUsage: 1, wantKinds: []store.MetricKind{store.MetricPartial}},
		{name: "refused partial falls back to unknown", refuse: store.MetricPartial, usage: leaderOnly, wantUsage: 1, wantKinds: []store.MetricKind{store.MetricUnknown}},
		{name: "nothing measured records nothing", usage: Usage{Wall: time.Second}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &usageStore{refuse: tc.refuse}
			RecordUsage(st)(t.Context(), n, tc.usage)
			if len(st.usage) != tc.wantUsage {
				t.Fatalf("usage writes = %+v", st.usage)
			}
			if tc.wantUsage == 1 && st.usage[0] != (store.NodeUsage{CPUTime: tc.usage.CPUTime, MaxRSSBytes: tc.usage.MaxRSSBytes, Wall: tc.usage.Wall}) {
				t.Fatalf("usage = %+v", st.usage[0])
			}
			if len(st.kinds) != len(tc.wantKinds) || (len(st.kinds) == 1 && st.kinds[0] != tc.wantKinds[0]) {
				t.Fatalf("markers = %v, want %v", st.kinds, tc.wantKinds)
			}
		})
	}
}
