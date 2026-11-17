package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
)

type accountDeletionFake struct {
	plan    *client.AccountDeletionPreview
	deleted bool
	id      string
	teams   []string
}

func (f *accountDeletionFake) Preview(context.Context) (client.AccountDeletionPreview, error) {
	if f.plan != nil {
		return *f.plan, nil
	}
	return client.AccountDeletionPreview{AccountID: "account-id", Teams: []string{"personal", "work"}}, nil
}

func (f *accountDeletionFake) Delete(_ context.Context, id string, teams []string) (client.AccountDeletionResult, error) {
	f.deleted, f.id, f.teams = true, id, teams
	return client.AccountDeletionResult{AccountID: id, DeletedTeams: teams}, nil
}

func TestAccountDeletePreviewAndExplicitConfirmation(t *testing.T) {
	for _, confirm := range []bool{false, true} {
		args := []string{"account", "delete", "--profile", "cloud", "--session-stdin", "-o", "pretty"}
		if confirm {
			args = append(args, "--confirm", "account-id", "--confirm-team", "personal", "--confirm-team", "work")
		}
		inv, err := clisurface.Parse("sparkwing", args)
		if err != nil {
			t.Fatal(err)
		}
		f := &accountDeletionFake{}
		var out bytes.Buffer
		if err := runAccountDeleteCommand(t.Context(), inv, &out, f); err != nil {
			t.Fatal(err)
		}
		if f.deleted != confirm {
			t.Fatalf("deleted=%t, confirm=%t", f.deleted, confirm)
		}
		if !confirm && (!strings.Contains(out.String(), "personal") || !strings.Contains(out.String(), "work") || !strings.Contains(out.String(), "runs and data")) {
			t.Fatalf("preview omits consequences: %s", out.String())
		}
		if confirm && (f.id != "account-id" || len(f.teams) != 2) {
			t.Fatalf("confirmation lost: %+v", f)
		}
	}
}

func TestAccountDeleteRequiresDirectSessionInput(t *testing.T) {
	if _, err := clisurface.Parse("sparkwing", []string{"account", "delete", "--profile", "cloud"}); err == nil || !strings.Contains(err.Error(), "session-stdin") {
		t.Fatalf("session input: %v", err)
	}
}

func TestAccountDeleteCLIThroughAuthenticatedController(t *testing.T) {
	f := newDeviceLoginFixtureAs(t, "approve", "owner")
	server := httptest.NewServer(f.handler)
	t.Cleanup(server.Close)
	d, err := client.NewAccountDeleter(t.Context(), client.EngineOptions{URL: server.URL, HTTP: &http.Client{Transport: &http.Transport{}}}, f.session)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := clisurface.Parse("sparkwing", []string{"account", "delete", "--profile", "cloud", "--session-stdin", "-o", "json"})
	if err != nil {
		t.Fatal(err)
	}
	var preview bytes.Buffer
	if err := runAccountDeleteCommand(t.Context(), inv, &preview, d); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(preview.String(), "device-test") {
		t.Fatal("preview omitted sole-member team")
	}
	if !strings.Contains(preview.String(), "35 days after creation") {
		t.Fatalf("preview omitted PostgreSQL archive expiry: %s", preview.String())
	}
	inv, err = clisurface.Parse("sparkwing", []string{"account", "delete", "--profile", "cloud", "--session-stdin", "--confirm", "member", "--confirm-team", "device-test", "-o", "json"})
	if err != nil {
		t.Fatal(err)
	}
	var result bytes.Buffer
	if err := runAccountDeleteCommand(t.Context(), inv, &result, d); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.String(), `"account_id":"member"`) {
		t.Fatalf("result=%s", result.String())
	}
	if _, err := f.state.AsOperator().LookupSession(t.Context(), f.session); err == nil {
		t.Fatal("CLI retained its session")
	}
}

func TestAccountDeletePlainPreviewIncludesBlockedTeams(t *testing.T) {
	for _, format := range []string{"plain", "json", "pretty"} {
		inv, err := clisurface.Parse("sparkwing", []string{"account", "delete", "--profile", "cloud", "--session-stdin", "-o", format})
		if err != nil {
			t.Fatal(err)
		}
		f := &accountDeletionFake{plan: &client.AccountDeletionPreview{AccountID: "account-id", Teams: []string{"personal"}, BlockedTeams: []string{"shared-team"}}}
		var out bytes.Buffer
		err = runAccountDeleteCommand(t.Context(), inv, &out, f)
		if err == nil || !strings.Contains(out.String()+err.Error(), "shared-team") {
			t.Fatalf("%s blocked preview: output=%s error=%v", format, out.String(), err)
		}
		if !strings.Contains(out.String(), "shared-team") {
			t.Fatalf("%s omitted blocked team: %s", format, out.String())
		}
		if f.deleted {
			t.Fatal("blocked preview deleted account")
		}
	}
}

func TestAccountDeleteNamesMissingConfirmationFlags(t *testing.T) {
	inv, err := clisurface.Parse("sparkwing", []string{"account", "delete", "--profile", "cloud", "--session-stdin", "--confirm", "account-id", "-o", "json"})
	if err != nil {
		t.Fatal(err)
	}
	f := &accountDeletionFake{}
	var out bytes.Buffer
	err = runAccountDeleteCommand(t.Context(), inv, &out, f)
	if err == nil || !strings.Contains(err.Error(), "--confirm-team") || f.deleted {
		t.Fatalf("missing flags: deleted=%t err=%v", f.deleted, err)
	}
}

func TestAccountDeletePromptsForFreshSignIn(t *testing.T) {
	for _, confirm := range []bool{false, true} {
		args := []string{"account", "delete", "--profile", "cloud", "--session-stdin", "-o", "json"}
		if confirm {
			args = append(args, "--confirm", "account-id", "--confirm-team", "personal")
		}
		inv, err := clisurface.Parse("sparkwing", args)
		if err != nil {
			t.Fatal(err)
		}
		f := &accountDeletionFake{plan: &client.AccountDeletionPreview{AccountID: "account-id", Teams: []string{"personal"}, SignInRequired: true}}
		var out bytes.Buffer
		err = runAccountDeleteCommand(t.Context(), inv, &out, f)
		if err == nil || !strings.Contains(err.Error(), "sign in again") || f.deleted {
			t.Fatalf("stale CLI: deleted=%t err=%v", f.deleted, err)
		}
	}
}

func TestAccountDeletePreviewWarnsAboutSharedCredentials(t *testing.T) {
	inv, err := clisurface.Parse("sparkwing", []string{"account", "delete", "--profile", "cloud", "--session-stdin", "-o", "pretty"})
	if err != nil {
		t.Fatal(err)
	}
	f := &accountDeletionFake{plan: &client.AccountDeletionPreview{AccountID: "account-id", ArchiveNotice: "archive notice from docs", CredentialNotice: "Runner and CI tokens in shared teams are revoked"}}
	var out bytes.Buffer
	if err := runAccountDeleteCommand(t.Context(), inv, &out, f); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Runner and CI tokens", "shared teams", "archive notice from docs", "account-deletion-retention"} {
		if !strings.Contains(out.String(), text) {
			t.Fatalf("preview omitted %q: %s", text, out.String())
		}
	}
}
