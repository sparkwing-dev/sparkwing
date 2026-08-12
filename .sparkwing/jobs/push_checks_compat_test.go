package jobs

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestFleetPushChecksDispatchesSparkwingPrePush(t *testing.T) {
	t.Parallel()
	_, ok := sparkwing.Lookup("push-checks")
	if !ok {
		t.Fatal("push-checks is not registered")
	}
}
