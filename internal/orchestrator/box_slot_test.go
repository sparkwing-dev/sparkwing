package orchestrator

import (
	"context"
	"os"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/boxslot"
)

func TestBoxSlotCap_Precedence(t *testing.T) {
	p := Paths{Root: t.TempDir()}
	os.Unsetenv("SPARKWING_BOX_SLOTS")

	slots, src := BoxSlotCap(p, 1)
	if src != "default" {
		t.Fatalf("source = %q, want default", src)
	}
	if want := boxslot.DefaultMaxSlots(1); slots != want {
		t.Fatalf("default cap = %d, want %d", slots, want)
	}

	t.Setenv("SPARKWING_BOX_SLOTS", "5")
	if slots, src = BoxSlotCap(p, 1); slots != 5 || src != "env" {
		t.Fatalf("env cap = %d,%q want 5,env", slots, src)
	}

	if err := boxslot.WriteControl(p.BoxSlotDir(), "2"); err != nil {
		t.Fatalf("WriteControl: %v", err)
	}
	if slots, src = BoxSlotCap(p, 1); slots != 2 || src != "control" {
		t.Fatalf("control cap = %d,%q want 2,control", slots, src)
	}

	if err := boxslot.WriteControl(p.BoxSlotDir(), "off"); err != nil {
		t.Fatalf("WriteControl off: %v", err)
	}
	if slots, src = BoxSlotCap(p, 1); slots > 0 || src != "control" {
		t.Fatalf("control off cap = %d,%q want <=0,control", slots, src)
	}
}

func TestAcquireBoxSlot_HonorsLiveControl(t *testing.T) {
	p := Paths{Root: t.TempDir()}
	os.Unsetenv("SPARKWING_BOX_SLOTS_PIN")
	os.Unsetenv("SPARKWING_BOX_NO_WAIT")
	os.Unsetenv("SPARKWING_BOX_SLOTS")
	if err := boxslot.WriteControl(p.BoxSlotDir(), "1"); err != nil {
		t.Fatalf("WriteControl: %v", err)
	}

	release, err := acquireBoxSlot(context.Background(), p, 1)
	if err != nil {
		t.Fatalf("acquireBoxSlot: %v", err)
	}
	defer release()

	st, err := boxslot.Status(p.BoxSlotDir())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.ActiveHolders != 1 {
		t.Fatalf("active holders = %d, want 1", st.ActiveHolders)
	}
}

func TestAcquireBoxSlot_PinOverridesControl(t *testing.T) {
	p := Paths{Root: t.TempDir()}
	os.Unsetenv("SPARKWING_BOX_NO_WAIT")
	os.Unsetenv("SPARKWING_BOX_SLOTS")
	if err := boxslot.WriteControl(p.BoxSlotDir(), "1"); err != nil {
		t.Fatalf("WriteControl: %v", err)
	}
	t.Setenv("SPARKWING_BOX_SLOTS_PIN", "off")

	release, err := acquireBoxSlot(context.Background(), p, 1)
	if err != nil {
		t.Fatalf("acquireBoxSlot: %v", err)
	}
	defer release()

	st, err := boxslot.Status(p.BoxSlotDir())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.ActiveHolders != 0 {
		t.Fatalf("active holders = %d, want 0 (an 'off' pin disables the semaphore over a live control)", st.ActiveHolders)
	}
}
