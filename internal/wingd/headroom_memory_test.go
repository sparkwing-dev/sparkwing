package wingd

import "testing"

// TestMemReserveAndExternal_ExhaustedHostLeavesNoHeadroom pins what a memory
// reading of zero has to do to admission, so the sampler's classification of
// zero cannot be reverted without this failing. A host reporting no available
// memory must charge its consumption as external pressure; a host whose memory
// was never read charges nothing, because admission may not hold a run back
// against pressure nobody looked at.
//
// The gap between the two is the whole reason the classification matters: the
// same exhausted machine grants an order of magnitude more memory when its
// reading is discarded than when it is believed.
func TestMemReserveAndExternal_ExhaustedHostLeavesNoHeadroom(t *testing.T) {
	const total = 16 << 30
	const owned = 2 << 30
	const reserveFraction = 0.1

	exhausted := HostStat{TotalMemoryBytes: total, FreeMemoryBytes: 0, MemoryMeasured: true}
	reserved, external := memReserveAndExternal(exhausted, owned, reserveFraction)
	if external == 0 {
		t.Fatal("an exhausted host charged no external memory; the reading was believed but not acted on")
	}
	measuredHeadroom := headroomFromReserveExternal(total, reserved, external)

	blind := HostStat{TotalMemoryBytes: total, FreeMemoryBytes: 0, MemoryMeasured: false}
	blindReserved, blindExternal := memReserveAndExternal(blind, owned, reserveFraction)
	if blindExternal != 0 {
		t.Fatalf("unread memory charged %d bytes of external pressure; a dimension nobody read charges nothing", blindExternal)
	}
	blindHeadroom := headroomFromReserveExternal(total, blindReserved, blindExternal)

	if measuredHeadroom >= blindHeadroom {
		t.Fatalf("exhausted host granted %d bytes, unread host granted %d; believing the reading must grant less",
			measuredHeadroom, blindHeadroom)
	}
}
