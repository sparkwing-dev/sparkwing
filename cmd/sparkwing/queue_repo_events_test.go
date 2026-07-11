package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func repoQueueState() wingwire.QueueState {
	qs := sampleQueueState()
	qs.Holders[0].Repo = "webapp"
	qs.Holders = append(qs.Holders, wingwire.Holder{
		RunID: "run-child", Pipeline: "deploy-child", Repo: "webapp",
		Parent: "run-holder", ElapsedMS: 30000,
	})
	qs.Waiters[0].Repo = "api-server"
	qs.Events = &wingwire.EventsWindow{
		WindowMS:      24 * 60 * 60 * 1000,
		Runs:          142,
		MedianWaitMS:  4000,
		Evictions:     []wingwire.EvictionCount{{Key: "land", Count: 3}},
		QueueTimeouts: 1,
	}
	return qs
}

func TestRenderQueue_PrettyShowsRepoColumn(t *testing.T) {
	var buf bytes.Buffer
	if err := renderQueue(&buf, repoQueueState(), "pretty"); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"REPO", "webapp", "api-server"} {
		if !strings.Contains(out, want) {
			t.Fatalf("pretty output missing %q:\n%s", want, out)
		}
	}
}

func TestRenderQueue_PrettyRendersRepolessRowsAsDash(t *testing.T) {
	var buf bytes.Buffer
	if err := renderQueue(&buf, sampleQueueState(), "pretty"); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	holderLine := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "run-holder") {
			holderLine = line
		}
	}
	if holderLine == "" || !strings.Contains(holderLine, "-") {
		t.Fatalf("repo-less holder row should carry a dash:\n%s", out)
	}
}

func TestRenderQueue_PrettyIndentsAttachedChildUnderParent(t *testing.T) {
	var buf bytes.Buffer
	if err := renderQueue(&buf, repoQueueState(), "pretty"); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	lines := strings.Split(out, "\n")
	parentIdx, childIdx := -1, -1
	for i, line := range lines {
		if strings.HasPrefix(line, "run-holder") {
			parentIdx = i
		}
		if strings.Contains(line, "run-child (attached)") {
			childIdx = i
			if !strings.HasPrefix(line, "  ") {
				t.Errorf("attached child row not indented: %q", line)
			}
		}
	}
	if parentIdx == -1 || childIdx == -1 {
		t.Fatalf("parent or child row missing:\n%s", out)
	}
	if childIdx != parentIdx+1 {
		t.Errorf("child row not directly under parent (parent line %d, child line %d):\n%s", parentIdx, childIdx, out)
	}
}

func TestRenderQueue_PrettyEventsLineOmitsZeroCategories(t *testing.T) {
	var buf bytes.Buffer
	if err := renderQueue(&buf, repoQueueState(), "pretty"); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "last 24h: 142 runs, median wait 4s, 3 evictions (key: land), 1 queue-timeout") {
		t.Fatalf("events line missing or wrong:\n%s", out)
	}
	if strings.Contains(out, "cancellation") {
		t.Fatalf("zero category rendered:\n%s", out)
	}
}

func TestRenderQueue_PrettyOmitsEventsLineWithoutWindow(t *testing.T) {
	var buf bytes.Buffer
	if err := renderQueue(&buf, sampleQueueState(), "pretty"); err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(buf.String(), "last 24h") {
		t.Fatalf("events line rendered without a window:\n%s", buf.String())
	}
}

func TestFmtEventsLine_SingularAndMultiKeyForms(t *testing.T) {
	line := fmtEventsLine(&wingwire.EventsWindow{
		WindowMS: 24 * 60 * 60 * 1000, Runs: 1, MedianWaitMS: 500,
		Evictions:     []wingwire.EvictionCount{{Key: "deploy", Count: 1}, {Key: "land", Count: 2}},
		Cancellations: 2,
	})
	want := "last 24h: 1 run, median wait 1s, 3 evictions (keys: deploy, land), 2 cancellations"
	if line != want {
		t.Errorf("fmtEventsLine = %q, want %q", line, want)
	}
	if got := fmtEventsLine(nil); got != "" {
		t.Errorf("nil window = %q, want empty", got)
	}
}

func TestRenderQueue_JSONCarriesRepoParentAndEvents(t *testing.T) {
	var buf bytes.Buffer
	if err := renderQueue(&buf, repoQueueState(), "json"); err != nil {
		t.Fatalf("render json: %v", err)
	}
	var got wingwire.QueueState
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("json invalid: %v", err)
	}
	if got.Holders[0].Repo != "webapp" || got.Waiters[0].Repo != "api-server" {
		t.Errorf("repo fields lost: %+v", got)
	}
	if got.Holders[1].Parent != "run-holder" {
		t.Errorf("child parent field lost: %+v", got.Holders[1])
	}
	if got.Events == nil || got.Events.Runs != 142 || got.Events.Evictions[0].Key != "land" {
		t.Errorf("events window lost: %+v", got.Events)
	}
}

func TestRenderQueue_PlainCarriesRepoAndParent(t *testing.T) {
	var buf bytes.Buffer
	if err := renderQueue(&buf, repoQueueState(), "plain"); err != nil {
		t.Fatalf("render plain: %v", err)
	}
	out := buf.String()
	var parentRec, childRec, waiterRec string
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "holder\trun-holder"):
			parentRec = line
		case strings.HasPrefix(line, "holder\trun-child"):
			childRec = line
		case strings.HasPrefix(line, "waiter\t"):
			waiterRec = line
		}
	}
	if !strings.Contains(parentRec, "\twebapp\t") {
		t.Errorf("holder record missing repo: %q", parentRec)
	}
	if !strings.HasSuffix(childRec, "\trun-holder") {
		t.Errorf("child record missing parent: %q", childRec)
	}
	if !strings.Contains(waiterRec, "\tapi-server\t") {
		t.Errorf("waiter record missing repo: %q", waiterRec)
	}
}
