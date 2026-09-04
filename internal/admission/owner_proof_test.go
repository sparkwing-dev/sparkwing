package admission

import "testing"

func TestProvesOwnerAcceptsAMemberAttachedToTheLease(t *testing.T) {
	l := testLedger(t, 4, 0)
	parent := mustGrant(t, l, Request{ID: "parent-run", Cores: 1})
	if err := l.Attach(parent.ID, "child-run"); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	if !l.ProvesOwner(parent.Token, "parent-run") {
		t.Fatal("the lease's own request could not prove ownership with its token")
	}
	if !l.ProvesOwner(parent.Token, "child-run") {
		t.Fatal("an attached member could not prove ownership with the token it was granted")
	}
	if l.ProvesOwner(parent.Token, "unrelated-run") {
		t.Fatal("a run outside the lease proved ownership")
	}
	if l.ProvesOwner("not-a-token", "parent-run") {
		t.Fatal("an unknown token proved ownership")
	}
}

func TestProvesOwnerRejectsAMemberAfterItReleases(t *testing.T) {
	l := testLedger(t, 4, 0)
	parent := mustGrant(t, l, Request{ID: "parent-run", Cores: 1})
	if err := l.Attach(parent.ID, "child-run"); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if _, err := l.Release(parent.ID, "child-run"); err != nil {
		t.Fatalf("Release: %v", err)
	}

	if l.ProvesOwner(parent.Token, "child-run") {
		t.Fatal("a released member still proved ownership")
	}
	if !l.ProvesOwner(parent.Token, "parent-run") {
		t.Fatal("the surviving request lost its own proof")
	}
}
