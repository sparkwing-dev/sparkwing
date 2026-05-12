package orchestrator

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

func mkRun(id, pipeline, status string, t time.Time) *store.Run {
	return &store.Run{ID: id, Pipeline: pipeline, Status: status, StartedAt: t}
}

func TestPivotByPipeline_GroupsAndOrdersByLastStarted(t *testing.T) {
	now := time.Now()
	runs := []*store.Run{
		mkRun("a1", "deploy-frontend", "success", now.Add(-time.Hour)),
		mkRun("a2", "deploy-frontend", "failed", now.Add(-30*time.Minute)),
		mkRun("b1", "release-cli", "success", now.Add(-2*time.Hour)),
	}
	got := pivotByPipeline(runs, 30)
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}
	if got[0].Pipeline != "deploy-frontend" {
		t.Errorf("most-recent should be first, got %q", got[0].Pipeline)
	}
	df := got[0]
	if df.Total != 2 || df.Failures != 1 {
		t.Errorf("counts wrong: %+v", df)
	}
	if df.LastRunID != "a2" || df.LastStatus != "failed" {
		t.Errorf("last-run wrong: %+v", df)
	}
}

func TestPivotByPipeline_HonorsSparklineCap(t *testing.T) {
	now := time.Now()
	runs := make([]*store.Run, 5)
	for i := range runs {
		runs[i] = mkRun("r", "p", "success", now.Add(-time.Duration(i)*time.Minute))
	}
	got := pivotByPipeline(runs, 3)
	if len(got[0].RecentStatuses) != 3 {
		t.Errorf("sparkline len = %d, want 3", len(got[0].RecentStatuses))
	}
}

func TestRenderSparkline_AsciiGlyphs(t *testing.T) {
	got := renderSparkline([]string{"success", "failed", "running", "cancelled"}, SparkAscii)
	for _, want := range []string{"✓", "✗", "⋯", "⊘"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestRenderPipelinePivot_JSONShape(t *testing.T) {
	now := time.Now()
	runs := []*store.Run{
		mkRun("a", "deploy", "success", now),
	}
	var buf bytes.Buffer
	if err := RenderPipelinePivot(runs,
		PivotOpts{SparklineLen: 5, Style: SparkAscii, JSON: true}, &buf); err != nil {
		t.Fatal(err)
	}
	var rows []PipelinePivotRow
	if err := json.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v\n%s", err, buf.String())
	}
	if len(rows) != 1 || rows[0].Pipeline != "deploy" || rows[0].LastRunID != "a" {
		t.Errorf("unexpected rows: %+v", rows)
	}
}

func TestRenderPipelinePivot_QuietPrintsPipelinesOnly(t *testing.T) {
	now := time.Now()
	runs := []*store.Run{
		mkRun("a", "deploy", "success", now),
		mkRun("b", "release", "failed", now.Add(-time.Minute)),
	}
	var buf bytes.Buffer
	if err := RenderPipelinePivot(runs,
		PivotOpts{SparklineLen: 5, Style: SparkAscii, Quiet: true}, &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Errorf("expected 2 lines, got %q", out)
	}
}

func TestRenderPipelinePivot_TableHasCounts(t *testing.T) {
	now := time.Now()
	runs := []*store.Run{
		mkRun("a", "deploy", "failed", now),
		mkRun("b", "deploy", "success", now.Add(-time.Hour)),
	}
	var buf bytes.Buffer
	if err := RenderPipelinePivot(runs,
		PivotOpts{SparklineLen: 30, Style: SparkAscii}, &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "PIPELINE") || !strings.Contains(out, "deploy") {
		t.Errorf("missing header/row: %s", out)
	}
}
