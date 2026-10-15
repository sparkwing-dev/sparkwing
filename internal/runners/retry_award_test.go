package runners

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func TestReviewRetryJobPassesHostingAwardValidation(t *testing.T) {
	boundary := errors.New("controller boundary")
	host := Hosting{
		ControllerURL: "https://controller.example",
		NewController: func(client.EngineOptions) (controllerwire.Controller, error) { return nil, boundary },
		Bind: func(context.Context, controllerwire.ClaimedUnit, controllerwire.Controller) (NativeServices, capability.Principal, error) {
			t.Fatal("crossed failed controller boundary")
			return NativeServices{}, capability.Principal{}, nil
		},
	}
	if err := host.Host(t.Context(), hostingAward(capability.UnitNode)); !errors.Is(err, boundary) {
		t.Fatalf("initial award did not reach controller: %v", err)
	}
	if err := quick.Check(func(identity uint64, mutation uint8) bool {
		unit := hostingAward(capability.UnitNode)
		unit.RunID = "retry-" + strconv.FormatUint(identity, 10)
		unit.Binding.RunID = unit.RunID
		unit.Request.RunID = "original-" + strconv.FormatUint(identity, 10)
		unit.Accepted.Spec.Request = unit.Request
		mutation %= 5
		switch mutation {
		case 1:
			unit.Binding.RunID = unit.Request.RunID
		case 2:
			unit.Accepted.Spec.Request.RunID += "foreign"
		case 3:
			unit.Accepted.Spec.Request.Request.Team += "foreign"
		case 4:
			unit.Accepted.Spec.Request.Hash += "foreign"
		}
		err := host.Host(t.Context(), unit)
		if errors.Is(err, boundary) != (mutation == 0) {
			t.Logf("retry mutation=%d crossed the wrong boundary: %v", mutation, err)
			return false
		}
		return true
	}, &quick.Config{MaxCount: 128}); err != nil {
		t.Fatal(err)
	}
}
