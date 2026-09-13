package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestGitHubWebhookBindings_RoundTrip(t *testing.T) {
	st := storetest.New(t).Open(t)
	ctx := context.Background()

	if err := st.PutGitHubWebhookBinding(ctx, store.GitHubWebhookBinding{
		Pipeline: "build", Repo: "Acme/Widgets", Secret: "s1",
		Events: []string{"push", "pull_request"}, HookID: 42,
	}); err != nil {
		t.Fatalf("PutGitHubWebhookBinding: %v", err)
	}

	got, err := st.GetGitHubWebhookBinding(ctx, "build", "acme/widgets")
	if err != nil {
		t.Fatalf("GetGitHubWebhookBinding: %v", err)
	}
	if got.Repo != "acme/widgets" || got.Secret != "s1" || got.HookID != 42 {
		t.Fatalf("binding = %+v", got)
	}
	if len(got.Events) != 2 || got.Events[0] != "push" || got.Events[1] != "pull_request" {
		t.Fatalf("events = %v", got.Events)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatalf("timestamps = %v / %v", got.CreatedAt, got.UpdatedAt)
	}

	// safety: the slug a caller supplies is folded, so a differently cased
	// delivery still resolves the row.
	if _, err := st.GetGitHubWebhookBinding(ctx, "build", "ACME/WIDGETS"); err != nil {
		t.Fatalf("case-folded lookup: %v", err)
	}
}

func TestGitHubWebhookBindings_ReconnectReplacesTheSecret(t *testing.T) {
	st := storetest.New(t).Open(t)
	ctx := context.Background()

	for _, secret := range []string{"first", "second"} {
		if err := st.PutGitHubWebhookBinding(ctx, store.GitHubWebhookBinding{
			Pipeline: "build", Repo: "acme/widgets", Secret: secret,
			Events: []string{"push"}, HookID: 7,
		}); err != nil {
			t.Fatalf("PutGitHubWebhookBinding(%s): %v", secret, err)
		}
	}
	bindings, err := st.ListGitHubWebhookBindings(ctx, "build")
	if err != nil {
		t.Fatalf("ListGitHubWebhookBindings: %v", err)
	}
	if len(bindings) != 1 {
		t.Fatalf("bindings = %d, want one row per pipeline and repository", len(bindings))
	}
	if bindings[0].Secret != "second" {
		t.Fatalf("secret = %q, want the reconnected one", bindings[0].Secret)
	}
}

func TestGitHubWebhookBindings_ListScopesToOnePipeline(t *testing.T) {
	st := storetest.New(t).Open(t)
	ctx := context.Background()

	for _, b := range []store.GitHubWebhookBinding{
		{Pipeline: "build", Repo: "acme/widgets", Secret: "s1"},
		{Pipeline: "build", Repo: "acme/gadgets", Secret: "s2"},
		{Pipeline: "deploy", Repo: "acme/widgets", Secret: "s3"},
	} {
		if err := st.PutGitHubWebhookBinding(ctx, b); err != nil {
			t.Fatalf("PutGitHubWebhookBinding(%s): %v", b.Pipeline, err)
		}
	}

	build, err := st.ListGitHubWebhookBindings(ctx, "build")
	if err != nil {
		t.Fatalf("ListGitHubWebhookBindings: %v", err)
	}
	if len(build) != 2 || build[0].Repo != "acme/gadgets" || build[1].Repo != "acme/widgets" {
		t.Fatalf("build bindings = %+v", build)
	}
	all, err := st.ListGitHubWebhookBindings(ctx, "")
	if err != nil {
		t.Fatalf("ListGitHubWebhookBindings(all): %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("all bindings = %d, want 3", len(all))
	}
}

func TestGitHubWebhookBindings_DeleteReportsWhetherARowWasThere(t *testing.T) {
	st := storetest.New(t).Open(t)
	ctx := context.Background()

	if err := st.PutGitHubWebhookBinding(ctx, store.GitHubWebhookBinding{
		Pipeline: "build", Repo: "acme/widgets", Secret: "s1",
	}); err != nil {
		t.Fatalf("PutGitHubWebhookBinding: %v", err)
	}
	removed, err := st.DeleteGitHubWebhookBinding(ctx, "build", "Acme/Widgets")
	if err != nil {
		t.Fatalf("DeleteGitHubWebhookBinding: %v", err)
	}
	if !removed {
		t.Fatal("delete reported no row for a binding that was stored")
	}
	if _, err := st.GetGitHubWebhookBinding(ctx, "build", "acme/widgets"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("read after delete = %v, want ErrNotFound", err)
	}
	removed, err = st.DeleteGitHubWebhookBinding(ctx, "build", "acme/widgets")
	if err != nil {
		t.Fatalf("second DeleteGitHubWebhookBinding: %v", err)
	}
	if removed {
		t.Fatal("delete reported a row for a binding that was already gone")
	}
}

func TestGitHubWebhookBindings_RefuseIncompleteRows(t *testing.T) {
	st := storetest.New(t).Open(t)
	ctx := context.Background()

	for name, b := range map[string]store.GitHubWebhookBinding{
		"no pipeline": {Repo: "acme/widgets", Secret: "s"},
		"no repo":     {Pipeline: "build", Secret: "s"},
		"no secret":   {Pipeline: "build", Repo: "acme/widgets"},
	} {
		if err := st.PutGitHubWebhookBinding(ctx, b); !errors.Is(err, store.ErrGitHubWebhookBinding) {
			t.Errorf("%s: err = %v, want ErrGitHubWebhookBinding", name, err)
		}
	}
}

// A store stamped at the version before the bindings table takes the
// migration on reopen, so an older controller's database connects.
func TestSchemaV37_UpgradeFromAStoreStampedAt36(t *testing.T) {
	target := storetest.New(t)
	seeded, err := target.TryOpen()
	if err != nil {
		t.Fatalf("Open#1: %v", err)
	}
	if _, err := seeded.DB().Exec(`DROP TABLE github_webhook_bindings`); err != nil {
		t.Fatalf("drop the bindings table: %v", err)
	}
	if _, err := seeded.DB().Exec(`DELETE FROM sparkwing_schema_version WHERE version >= 37`); err != nil {
		t.Fatalf("reset version to 36: %v", err)
	}
	if v := readSchemaVersion(t, seeded.DB()); v != 36 {
		t.Fatalf("seeded version = %d, want 36", v)
	}
	_ = seeded.Close()

	upgraded, err := target.TryOpen()
	if err != nil {
		t.Fatalf("Open#2 (upgrade): %v", err)
	}
	defer func() { _ = upgraded.Close() }()
	if v := readSchemaVersion(t, upgraded.DB()); v != store.ExpectedSchemaVersion() {
		t.Fatalf("version after upgrade = %d, want %d", v, store.ExpectedSchemaVersion())
	}
	ctx := context.Background()
	if err := upgraded.PutGitHubWebhookBinding(ctx, store.GitHubWebhookBinding{
		Pipeline: "build", Repo: "acme/widgets", Secret: "s1",
	}); err != nil {
		t.Fatalf("PutGitHubWebhookBinding after upgrade: %v", err)
	}
}
