package nodehost

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

type expansionDelegatorFixture struct {
	ownerDelegatorFixture
	corrupt func(*capability.Capability)
}

func (f expansionDelegatorFixture) DelegateCapability(ctx context.Context, spec capability.DelegateSpec) (capability.Issued, error) {
	issued, err := f.ownerDelegatorFixture.DelegateCapability(ctx, spec)
	f.corrupt(&issued.Capability)
	return issued, err
}

func FuzzOwnerExpansionPreservesDelegatedSource(f *testing.F) {
	for field := uint8(0); field < 12; field++ {
		f.Add(field, "changed")
	}
	f.Fuzz(func(t *testing.T, field uint8, value string) {
		_, host, input := newOwnerFixture(t, true)
		row := runstate.NodeRow{Ref: runspec.NodeRef{RunID: runspec.RunID(input.RunID), NodeID: "source"}, Generation: 3, Attempt: 7}
		host.Delegator = expansionDelegatorFixture{ownerDelegatorFixture{actor: input.Owner}, func(child *capability.Capability) {
			switch field % 12 {
			case 0:
				child.Prefix = ""
			case 1:
				child.Parent = nil
			case 2:
				child.Audience = capability.NodeHost
			case 3:
				child.Binding.RunID += ":" + value
			case 4:
				child.Binding.NodeID += ":" + value
			case 5:
				child.Binding.Generation++
			case 6:
				child.Binding.Attempt++
			case 7:
				child.Binding.Holder += ":" + value
			case 8:
				child.Binding.Expansion.ID += ":" + value
			case 9:
				child.Binding.Expansion.Generation++
			case 10:
				child.Binding.Expansion.Holder += ":" + value
			case 11:
				child.Grants = append(child.Grants, capability.NodeReport)
			}
		}}
		if _, err := host.expansionActor(t.Context(), input, row); err == nil {
			t.Fatal("changed delegation authorized expansion submission")
		}
	})
}
