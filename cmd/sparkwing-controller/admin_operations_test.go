package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestAdminRequiresOperatorIdentity(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	if err := openState(t).Close(); err != nil {
		t.Fatal(err)
	}
	if err := runAdmin([]string{"freeze", "--team", "default", "--reason", "incident"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "--operator") {
		t.Fatalf("missing operator: %v", err)
	}
}

func TestAdminAuditsOperatorAndScopesLargeGrantToInvocation(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	t.Setenv("SPARKWING_ADMIN_GRANT_ALLOW_LARGE", "1")
	st := openState(t)
	if _, err := teststore.SQL(t, st).ExecContext(t.Context(), `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('ops-alice','alice@example.test',1,1),('ops-bob','bob@example.test',1,1)`); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"freeze", "unfreeze", "grant"} {
		args := []string{verb, "--team", "default", "--reason", "incident", "--operator", "ops-alice"}
		if verb == "grant" {
			args = append(args, "--cents", "10001", "--allow-large")
		}
		var audit bytes.Buffer
		if err := runAdmin(args, &bytes.Buffer{}, &audit); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(audit.String(), `"principal_id":"ops-alice"`) {
			t.Errorf("audit: %s", &audit)
		}
	}
	if err := runAdmin([]string{"grant", "--team", "default", "--reason", "incident", "--operator", "ops-bob", "--cents", "10001"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "--allow-large") {
		t.Fatalf("pod environment authorized a later large grant: %v", err)
	}
	st = openState(t)
	defer func() { _ = st.Close() }()
	tenant := teststore.ForTeam(t, st, store.DefaultTeam)
	events, err := tenant.BusinessEvents(t.Context(), "", 100, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("events: %+v", events)
	}
	for _, event := range events {
		if event.Actor != "ops-alice" {
			t.Errorf("audit actor: %+v", event)
		}
	}
}
