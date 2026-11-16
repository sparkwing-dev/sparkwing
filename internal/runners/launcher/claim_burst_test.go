package launcher

import (
	"context"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type queuedControl struct {
	*controlFake
	queue     []*CloudJobAward
	claimedAt []time.Time
	stopAfter int
	cancel    context.CancelFunc
}

func (q *queuedControl) Claim(context.Context, string, []string) (*CloudJobAward, time.Duration, error) {
	q.claimedAt = append(q.claimedAt, time.Now())
	if len(q.claimedAt) == q.stopAfter {
		q.cancel()
	}
	if len(q.queue) == 0 {
		return nil, 0, nil
	}
	award := q.queue[0]
	q.queue = q.queue[1:]
	return award, 0, nil
}

func fanOut(base *CloudJobAward, n int) []*CloudJobAward {
	awards := make([]*CloudJobAward, n)
	for i := range awards {
		award := *base
		award.Binding.UnitID = fmt.Sprintf("unit-%d", i)
		award.Binding.NodeID = runspec.NodeID(fmt.Sprintf("job-%d", i))
		awards[i] = &award
	}
	return awards
}

func TestLauncherLaunchesWorkReadyTogetherInOneCycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l, _, _, fake := fixture()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		control := &queuedControl{controlFake: fake, queue: fanOut(fake.award, 3), stopAfter: 4, cancel: cancel}
		l.Control, l.Poll = control, 2*time.Second
		if err := l.Run(ctx); err != nil {
			t.Fatal(err)
		}
		if len(control.claimedAt) != 4 {
			t.Fatalf("claims=%d, want three awards and one empty answer", len(control.claimedAt))
		}
		if spread := control.claimedAt[3].Sub(control.claimedAt[0]); spread >= l.Poll {
			t.Fatalf("three jobs ready together were claimed across %s, at least one %s poll apart", spread, l.Poll)
		}
	})
}

func TestLauncherBoundsClaimsPerCycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l, _, _, fake := fixture()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		control := &queuedControl{controlFake: fake, queue: fanOut(fake.award, maxClaimsPerCycle+2), stopAfter: maxClaimsPerCycle + 1, cancel: cancel}
		l.Control, l.Poll = control, 2*time.Second
		if err := l.Run(ctx); err != nil {
			t.Fatal(err)
		}
		first := control.claimedAt[:maxClaimsPerCycle]
		if spread := first[len(first)-1].Sub(first[0]); spread >= l.Poll {
			t.Fatalf("the first %d claims spread across %s", maxClaimsPerCycle, spread)
		}
		if gap := control.claimedAt[maxClaimsPerCycle].Sub(first[0]); gap < l.Poll {
			t.Fatalf("claim %d came %s after the cycle began; a cycle claims at most %d before waiting one %s poll", maxClaimsPerCycle+1, gap, maxClaimsPerCycle, l.Poll)
		}
	})
}
