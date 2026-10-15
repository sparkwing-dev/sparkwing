//go:build rewrite_integration

package runners

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func TestReviewRetryJobPassesHostingAwardValidation(t *testing.T) {
	boundary := errors.New("controller boundary")
	host := Hosting{
		ControllerURL: "https://controller.example", Lease: time.Minute, Interval: 10 * time.Second,
		NewController: func(client.EngineOptions) (controllerwire.Controller, error) { return nil, boundary },
		PrepareNode: func(context.Context, controllerwire.ClaimedUnit, controllerwire.Controller) (nodehost.NodeHost, nodehost.NodeInput, func() error, error) {
			t.Fatal("crossed failed controller boundary")
			return nil, nodehost.NodeInput{}, nil, nil
		},
	}
	if err := host.Host(t.Context(), hostingAward(capability.UnitNode)); !errors.Is(err, boundary) {
		t.Fatalf("initial award did not reach controller: %v", err)
	}
	if err := quick.Check(func(identity uint64) bool {
		unit := hostingAward(capability.UnitNode)
		unit.RunID = "retry-" + strconv.FormatUint(identity, 10)
		unit.Binding.RunID = unit.RunID
		unit.Request.RunID = "original-" + strconv.FormatUint(identity, 10)
		unit.Accepted.Spec.Request = unit.Request
		err := host.Host(t.Context(), unit)
		if !errors.Is(err, boundary) {
			t.Logf("retry rejected before controller: %v", err)
			return false
		}
		return true
	}, &quick.Config{MaxCount: 128}); err != nil {
		t.Fatal(err)
	}
}
