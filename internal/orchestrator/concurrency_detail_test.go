package orchestrator

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestConcWaitDetail(t *testing.T) {
	cases := []struct {
		name  string
		resp  store.AcquireSlotResponse
		leadR string
		leadN string
		want  string
	}{
		{
			name: "queued reports position and holder",
			resp: store.AcquireSlotResponse{
				Kind:     store.AcquireQueued,
				Position: 2,
				Holders:  []store.ConcurrencyHolder{{RunID: "r1", NodeID: "deploy"}},
			},
			want: "queued in deploy-prod: 2 ahead, held by r1/deploy",
		},
		{
			name: "queued with extra holders summarizes count",
			resp: store.AcquireSlotResponse{
				Kind:     store.AcquireQueued,
				Position: 0,
				Holders: []store.ConcurrencyHolder{
					{RunID: "r1", NodeID: "a"}, {RunID: "r2", NodeID: "b"},
				},
			},
			want: "queued in deploy-prod: 0 ahead, held by r1/a +1",
		},
		{
			name:  "coalesced reports leader",
			resp:  store.AcquireSlotResponse{Kind: store.AcquireCoalesced},
			leadR: "r9", leadN: "build",
			want: "coalescing in deploy-prod behind r9/build",
		},
		{
			name: "granted is not a wait",
			resp: store.AcquireSlotResponse{Kind: store.AcquireGranted},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := concWaitDetail("deploy-prod", tc.resp, tc.leadR, tc.leadN)
			if got != tc.want {
				t.Fatalf("concWaitDetail = %q, want %q", got, tc.want)
			}
		})
	}
}
