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
	deleted bool
	id      string
	teams   []string
}

func (f *accountDeletionFake) Preview(context.Context) (client.AccountDeletionPreview, error) {
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
