package wingd_test

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// TestQueueState_NamesBudgetSource checks the daemon reports which
// setting its budget came from. An operator reading the queue to find out
// why admission is capped is often not the person who spawned the daemon,
// so a budget with no source attached is one they can neither trust nor
// revoke.
func TestQueueState_NamesBudgetSource(t *testing.T) {
	home := shortHome(t)
	budget, err := wingd.ParseBudget("50%,ignore-external")
	if err != nil {
		t.Fatalf("parse budget: %v", err)
	}
	startDaemon(t, wingd.Config{
		Home:         home,
		Budget:       budget,
		BudgetSource: wingd.BudgetSourceConfig,
		BudgetOrigin: "/home/op/.config/sparkwing/budget",
	})

	qs, err := client.Query(context.Background(), client.Options{Home: home, Version: "v1.0.0"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if qs.Budget == nil {
		t.Fatal("queue state carries no budget")
	}
	if qs.Budget.Source != string(wingwire.BudgetSourceConfig) {
		t.Errorf("source = %q, want %q", qs.Budget.Source, wingwire.BudgetSourceConfig)
	}
	if qs.Budget.Origin != "/home/op/.config/sparkwing/budget" {
		t.Errorf("origin = %q, want the config path", qs.Budget.Origin)
	}
	if !qs.Budget.IgnoreExternal {
		t.Error("budget state does not report ignore-external")
	}
	if qs.Budget.Raw != "50%,ignore-external" {
		t.Errorf("raw = %q, want the setting as written", qs.Budget.Raw)
	}
}

// TestQueueState_UnsetBudgetIsReported is the negative control at the
// daemon boundary: with no budget configured, the queue state must still
// carry a budget row saying so. Reporting nothing leaves a reader unable
// to tell an unbudgeted machine from a daemon that simply never mentioned
// its budget, and those want opposite responses.
func TestQueueState_UnsetBudgetIsReported(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})

	qs, err := client.Query(context.Background(), client.Options{Home: home, Version: "v1.0.0"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if qs.Budget == nil {
		t.Fatal("queue state carries no budget row with no budget set; an unbudgeted machine must say so rather than stay silent")
	}
	if qs.Budget.Source != string(wingwire.BudgetSourceUnset) {
		t.Errorf("source = %q, want %q", qs.Budget.Source, wingwire.BudgetSourceUnset)
	}
	if qs.Budget.Origin != "" {
		t.Errorf("origin = %q, want empty: there is no setting to name", qs.Budget.Origin)
	}
}

// TestQueueState_UnrecordedBudgetSourceIsNotGuessed checks a daemon whose
// budget came in without a recorded source says unknown rather than
// naming one. A view that guesses sends an operator to edit a setting
// that would not change the budget.
func TestQueueState_UnrecordedBudgetSourceIsNotGuessed(t *testing.T) {
	home := shortHome(t)
	budget, err := wingd.ParseBudget("4")
	if err != nil {
		t.Fatalf("parse budget: %v", err)
	}
	startDaemon(t, wingd.Config{Home: home, Budget: budget})

	qs, err := client.Query(context.Background(), client.Options{Home: home, Version: "v1.0.0"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if qs.Budget == nil {
		t.Fatal("queue state carries no budget")
	}
	if qs.Budget.Source != string(wingwire.BudgetSourceUnknown) {
		t.Errorf("source = %q, want %q", qs.Budget.Source, wingwire.BudgetSourceUnknown)
	}
}
