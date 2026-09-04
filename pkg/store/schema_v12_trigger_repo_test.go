package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/storetest"
)

func TestSchemaV12_UpgradePreservesImplicitAwaitRepositoryProvenance(t *testing.T) {
	target := storetest.New(t)
	st, err := target.TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`ALTER TABLE triggers DROP COLUMN repo_inherited`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`DELETE FROM sparkwing_schema_version WHERE version >= 12`); err != nil {
		t.Fatal(err)
	}
	deleteFleetRequirements(t, st.DB())
	_ = st.Close()

	up, err := target.TryOpen()
	if err != nil {
		t.Fatalf("upgrade v11 store: %v", err)
	}
	defer func() { _ = up.Close() }()
	ctx := context.Background()
	trigger := store.Trigger{
		ID: "implicit", Pipeline: "child", CreatedAt: time.Now(),
		Repo: "owner/repo", RepoInherited: true,
	}
	if err := up.CreateTrigger(ctx, trigger); err != nil {
		t.Fatalf("CreateTrigger after upgrade: %v", err)
	}
	claimed, err := up.ClaimNextTrigger(ctx, time.Minute)
	if err != nil {
		t.Fatalf("ClaimNextTrigger after upgrade: %v", err)
	}
	if !claimed.RepoInherited {
		t.Fatal("claimed trigger lost RepoInherited")
	}
	got, err := up.GetTrigger(ctx, trigger.ID)
	if err != nil {
		t.Fatalf("GetTrigger after upgrade: %v", err)
	}
	if !got.RepoInherited {
		t.Fatal("fetched trigger lost RepoInherited")
	}
}
