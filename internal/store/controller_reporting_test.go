package store

import (
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestLogSigningProjectionRequiresTheExactCurrentWriter(t *testing.T) {
	_, tenant, actor, attempt := resultFixture(t)
	subject := blob.LogSubject{Kind: blob.LogJobAttempt, Team: attempt.Team, RunID: attempt.RunID, Attempt: &attempt}
	class, err := tenant.PrepareLogWrite(t.Context(), subject, actor)
	if err != nil || class != "r30" {
		t.Fatalf("class=%q: %v", class, err)
	}
	property := func(choice uint8, noise string) bool {
		wrong := subject
		identity := attempt
		wrong.Attempt = &identity
		writer := actor
		switch choice % 7 {
		case 0:
			wrong.Team += noise + "x"
		case 1:
			wrong.RunID += noise + "x"
			identity.RunID = wrong.RunID
		case 2:
			identity.NodeID += noise + "x"
		case 3:
			identity.Attempt++
		case 4:
			identity.Generation++
		case 5:
			writer.Holder += noise + "x"
		case 6:
			writer.Generation++
		}
		_, err := tenant.PrepareLogWrite(t.Context(), wrong, writer)
		return err != nil
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 50}); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.PrepareLogWrite(t.Context(), subject, actor); err != nil {
		t.Fatal(err)
	}
}

func TestActivityRequiresTheCurrentWriter(t *testing.T) {
	s, tenant, actor, attempt := resultFixture(t)
	ref := controllerwire.NodeRef{RunID: runspec.RunID(attempt.RunID), NodeID: runspec.NodeID(attempt.NodeID)}
	if err := tenant.WriteActivity(t.Context(), ref, actor, "compiling"); err != nil {
		t.Fatal(err)
	}
	actor.Generation++
	if err := tenant.WriteActivity(t.Context(), ref, actor, "forged"); err == nil {
		t.Fatal("stale activity persisted")
	}
	var detail string
	if err := s.DB().QueryRow(`SELECT status_detail FROM nodes WHERE team='a' AND run_id='run' AND node_id='job'`).Scan(&detail); err != nil || detail != "compiling" {
		t.Fatalf("detail=%q: %v", detail, err)
	}
}
