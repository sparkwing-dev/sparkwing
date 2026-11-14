package localcontroller

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

func TestBlockedRunEventDeliveryDoesNotStallMaintenance(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var completions atomic.Int32
		delivering := make(chan struct{})
		c := openFixture(t, func(_ context.Context, b Binding) (Services, error) {
			services := serviceFixture(b)
			services.Complete = func(context.Context) error {
				completions.Add(1)
				return nil
			}
			services.Deliver = func(ctx context.Context) error {
				close(delivering)
				<-ctx.Done()
				return nil
			}
			return services, nil
		})
		if _, err := c.Reconcile(t.Context()); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		joined := make(chan error, 1)
		go func() { joined <- c.Run(ctx, func(wingd.LocalActivity) error { return nil }) }()
		<-delivering
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if n := completions.Load(); n < 5 {
			t.Fatalf("maintenance ran %d times in 5s while delivery was blocked", n)
		}
		cancel()
		if err := <-joined; err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	})
}
