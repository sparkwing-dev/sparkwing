package store_test

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestGitHubDeliveryClaimsRotateTeamsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inbox.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	now := time.Unix(1_000_000, 0)
	for i, team := range []store.Team{"a", "b"} {
		installation := store.GitHubAppInstallation{InstallationID: int64(i + 1), AccountID: int64(i + 1), AccountLogin: string(team), AccountType: "Organization"}
		if _, err := teamHandle(t, st, team).TestOnlySeedGitHubInstallation(ctx, installation, now); err != nil {
			t.Fatal(err)
		}
		count := 12
		if team == "b" {
			count = 2
		}
		for n := range count {
			id := fmt.Sprintf("%s-%02d", team, n)
			body := fmt.Appendf(nil, `{"installation":{"id":%d},"delivery":%q}`, installation.InstallationID, id)
			if err := st.AsOperator().EnqueueGitHubDelivery(ctx, id, "push", body, now.Add(time.Duration(i*100+n)*time.Second)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := st.AsOperator().UnbindGitHubAppInstallation(ctx, 1); err != nil {
		t.Fatal(err)
	}
	for turn, want := range []string{"a-00", "b-00", "a-01", "b-01", "a-02"} {
		at := now.Add(time.Duration(turn+1) * time.Hour)
		claim, err := st.AsOperator().ClaimGitHubDelivery(ctx, at)
		if err != nil || claim == nil || claim.ID != want {
			t.Fatalf("turn %d: claim = %+v, %v, want %s", turn, claim, err, want)
		}
		reason := ""
		if claim.ID == "a-00" {
			reason = "team allowance exhausted"
		}
		if err := st.AsOperator().FinishGitHubDelivery(ctx, *claim, reason, at); err != nil {
			t.Fatal(err)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		st, err = store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestGitHubDeliveryUnboundInstallationsShareOneTurn(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "inbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := t.Context()
	now := time.Unix(1_000_000, 0)
	if _, err := teamHandle(t, st, "a").TestOnlySeedGitHubInstallation(ctx, store.GitHubAppInstallation{InstallationID: 1, AccountID: 1, AccountLogin: "a", AccountType: "Organization"}, now); err != nil {
		t.Fatal(err)
	}
	for i, body := range []string{`{}`, `{"installation":{"id":98}}`, `{"installation":{"id":99}}`, `{"installation":{"id":1}}`} {
		if err := st.AsOperator().EnqueueGitHubDelivery(ctx, fmt.Sprint(i), "push", []byte(body), now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	for turn, want := range []string{"3", "0", "1", "2"} {
		at := now.Add(time.Duration(turn+1) * time.Hour)
		claim, err := st.AsOperator().ClaimGitHubDelivery(ctx, at)
		if err != nil || claim == nil || claim.ID != want {
			t.Fatalf("claim = %+v, %v, want %s", claim, err, want)
		}
		reason := ""
		if claim.ID == "3" {
			reason = "team allowance exhausted"
		}
		if err := st.AsOperator().FinishGitHubDelivery(ctx, *claim, reason, at); err != nil {
			t.Fatal(err)
		}
	}
}
