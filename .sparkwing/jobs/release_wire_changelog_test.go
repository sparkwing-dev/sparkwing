package jobs

import (
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

const shapesBefore = `{"types":[
  {"type":"grant","fields":[
    {"name":"run_id","kind":"string","omitempty":false},
    {"name":"resources","kind":"struct","omitempty":false,"fields":[
      {"name":"cores","kind":"float64","omitempty":true}
    ]}
  ]},
  {"type":"stats_reset","fields":[]}
]}`

func TestWireShapeCuts_GrowthIsNotACut(t *testing.T) {
	after := `{"types":[
  {"type":"grant","fields":[
    {"name":"run_id","kind":"string","omitempty":false},
    {"name":"semaphores","kind":"[]string","omitempty":true},
    {"name":"resources","kind":"struct","omitempty":false,"fields":[
      {"name":"cores","kind":"float64","omitempty":true},
      {"name":"memory_bytes","kind":"int64","omitempty":true}
    ]}
  ]},
  {"type":"stats_reset","fields":[]},
  {"type":"unsupported","fields":[{"name":"type","kind":"string","omitempty":false}]}
]}`
	cuts, err := wireShapeCuts(shapesBefore, after)
	if err != nil {
		t.Fatalf("wireShapeCuts: %v", err)
	}
	if len(cuts) != 0 {
		t.Errorf("added types and fields read as cuts: %v", cuts)
	}
}

func TestWireShapeCuts_RemovalAndRetypeAreCuts(t *testing.T) {
	after := `{"types":[
  {"type":"grant","fields":[
    {"name":"run_id","kind":"string","omitempty":false},
    {"name":"resources","kind":"struct","omitempty":false,"fields":[
      {"name":"cores","kind":"int64","omitempty":true}
    ]}
  ]}
]}`
	cuts, err := wireShapeCuts(shapesBefore, after)
	if err != nil {
		t.Fatalf("wireShapeCuts: %v", err)
	}
	want := []string{
		"wingwire grant.resources.cores retyped float64,omitempty -> int64,omitempty",
		"wingwire stats_reset removed",
	}
	if !reflect.DeepEqual(cuts, want) {
		t.Errorf("cuts = %v, want %v", cuts, want)
	}
}

const specBefore = `openapi: 3.0.3
paths:
  /api/v1/runs:
    get:
      summary: List runs.
    post:
      summary: Create a run.
  /api/v1/runs/{id}:
    get:
      summary: Get a run.
components:
  schemas:
    Run:
      properties:
        id: {type: string}
        status: {type: string}
`

func TestAPISurfaceCuts_GrowthIsNotACut(t *testing.T) {
	after := specBefore + `    Node:
      properties:
        id: {type: string}
`
	cuts, err := apiSurfaceCuts(specBefore, after)
	if err != nil {
		t.Fatalf("apiSurfaceCuts: %v", err)
	}
	if len(cuts) != 0 {
		t.Errorf("an added schema read as a cut: %v", cuts)
	}
}

func TestAPISurfaceCuts_RemovedRouteMethodAndPropertyAreCuts(t *testing.T) {
	after := `openapi: 3.0.3
paths:
  /api/v1/runs:
    get:
      summary: List runs.
components:
  schemas:
    Run:
      properties:
        id: {type: string}
`
	cuts, err := apiSurfaceCuts(specBefore, after)
	if err != nil {
		t.Fatalf("apiSurfaceCuts: %v", err)
	}
	want := []string{
		"api /api/v1/runs/{id} removed",
		"api GET /api/v1/runs/{id} removed",
		"api POST /api/v1/runs removed",
		"api components.schemas.Run.properties.status removed",
	}
	if !reflect.DeepEqual(cuts, want) {
		t.Errorf("cuts = %v, want %v", cuts, want)
	}
}

func TestProtocolFloorCuts_OnlyARaisedFloorCuts(t *testing.T) {
	cases := []struct {
		name     string
		prev     protocolMajors
		cur      protocolMajors
		wantCuts int
	}{
		{name: "unchanged", prev: protocolMajors{Newest: 3, Floor: 1}, cur: protocolMajors{Newest: 3, Floor: 1}},
		{name: "new generation", prev: protocolMajors{Newest: 3, Floor: 1}, cur: protocolMajors{Newest: 4, Floor: 1}},
		{name: "raised floor", prev: protocolMajors{Newest: 3, Floor: 1}, cur: protocolMajors{Newest: 4, Floor: 2}, wantCuts: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := protocolFloorCuts(tc.prev, tc.cur); len(got) != tc.wantCuts {
				t.Errorf("protocolFloorCuts = %v, want %d cut(s)", got, tc.wantCuts)
			}
		})
	}
}

func TestParseProtocolMajors(t *testing.T) {
	src := "package wingwire\n\nconst ProtocolMajor = 3\n\nconst MinProtocolMajor = 1\n"
	got, err := parseProtocolMajors(src)
	if err != nil {
		t.Fatalf("parseProtocolMajors: %v", err)
	}
	if want := (protocolMajors{Newest: 3, Floor: 1}); got != want {
		t.Errorf("majors = %+v, want %+v", got, want)
	}
	if _, err := parseProtocolMajors("package wingwire\n"); err == nil {
		t.Error("a source without the constants should fail the gate, not read as zero")
	}
}

var wireMigrations = fstest.MapFS{
	"_unreleased.md": {Data: []byte("# Unreleased\n\n## Protocol floor raised to 2\n\nWhat to do.\n")},
	"v0.41.0.md":     {Data: []byte("# v0.41.0\n\n## Protocol floor raised to 2\n\nWhat to do.\n")},
}

func TestLintWireBreak_GrowthPasses(t *testing.T) {
	body := "## [Unreleased]\n\n### Added\n\n- **wingd:** A new message.\n"
	if issues := LintWireBreak(body, "v0.41.0", nil, wireMigrations); len(issues) != 0 {
		t.Fatalf("growth should pass, got %v", formatAllIssues(issues))
	}
}

func TestLintWireBreak_DeclaredCutPasses(t *testing.T) {
	body := "## [Unreleased]\n\n### Removed\n\n" +
		"- **wingd (Breaking):** The protocol floor rises to 2 and the wire drops `stats_reset`. " +
		"See [migration](docs/migrations/_unreleased.md#protocol-floor-raised-to-2).\n"
	if issues := LintWireBreak(body, "v0.41.0", []string{"wingwire stats_reset removed"}, wireMigrations); len(issues) != 0 {
		t.Fatalf("declared cut should pass, got %v", formatAllIssues(issues))
	}
}

func TestLintWireBreak_DeclaredCutInAVersionSectionPasses(t *testing.T) {
	body := "## [v0.41.0] - 2026-09-02\n\n### Removed\n\n" +
		"- **wingd (Breaking):** The wire drops the `stats_reset` route. " +
		"See [migration](docs/migrations/v0.41.0.md#protocol-floor-raised-to-2).\n"
	if issues := LintWireBreak(body, "v0.41.0", []string{"api POST /api/v1/runs removed"}, wireMigrations); len(issues) != 0 {
		t.Fatalf("declared cut should pass, got %v", formatAllIssues(issues))
	}
}

func TestLintWireBreak_UndeclaredCutFails(t *testing.T) {
	body := "## [Unreleased]\n\n### Changed\n\n- **wingd:** The wire drops `stats_reset`.\n"
	issues := LintWireBreak(body, "v0.41.0", []string{"wingwire stats_reset removed"}, wireMigrations)
	if len(issues) != 1 {
		t.Fatalf("issues = %v, want one unmarked-wire-break", formatAllIssues(issues))
	}
	if issues[0].Category != wireBreakCategory {
		t.Errorf("category = %q, want %q", issues[0].Category, wireBreakCategory)
	}
	if !strings.Contains(issues[0].Message, "stats_reset") {
		t.Errorf("message does not name the cut: %s", issues[0].Message)
	}
}

func TestLintWireBreak_BreakingEntryAboutSomethingElseFails(t *testing.T) {
	body := "## [Unreleased]\n\n### Removed\n\n" +
		"- **store (Breaking):** The runs table drops a column. " +
		"See [migration](docs/migrations/_unreleased.md#protocol-floor-raised-to-2).\n"
	issues := LintWireBreak(body, "v0.41.0", []string{"wingwire stats_reset removed"}, wireMigrations)
	if len(issues) != 1 || issues[0].Category != wireBreakCategory {
		t.Fatalf("issues = %v, want one unmarked-wire-break", formatAllIssues(issues))
	}
}

func TestLintWireBreak_CutWithoutAMigrationLinkFails(t *testing.T) {
	body := "## [Unreleased]\n\n### Removed\n\n- **wingd (Breaking):** The wire drops `stats_reset`.\n"
	issues := LintWireBreak(body, "v0.41.0", []string{"wingwire stats_reset removed"}, wireMigrations)
	if len(issues) != 1 || issues[0].Category != wireMigrationCategory {
		t.Fatalf("issues = %v, want one missing-wire-migration", formatAllIssues(issues))
	}
}

func TestLintWireBreak_CutLinkingAnAbsentSectionFails(t *testing.T) {
	body := "## [Unreleased]\n\n### Removed\n\n" +
		"- **wingd (Breaking):** The wire drops `stats_reset`. " +
		"See [migration](docs/migrations/_unreleased.md#no-such-heading).\n"
	issues := LintWireBreak(body, "v0.41.0", []string{"wingwire stats_reset removed"}, wireMigrations)
	if len(issues) != 1 || issues[0].Category != "missing-migration-anchor" {
		t.Fatalf("issues = %v, want one missing-migration-anchor", formatAllIssues(issues))
	}
}

func TestWireCuts_SkipsASurfaceThePreviousTagLacks(t *testing.T) {
	states := []wireSurfaceState{
		{surface: wireSurfaces[0]},
		{
			surface: wireSurfaces[2],
			present: true,
			prev:    "const ProtocolMajor = 3\nconst MinProtocolMajor = 1\n",
			cur:     "const ProtocolMajor = 3\nconst MinProtocolMajor = 1\n",
		},
	}
	cuts, err := wireCuts(states)
	if err != nil {
		t.Fatalf("wireCuts: %v", err)
	}
	if len(cuts) != 0 {
		t.Errorf("a tag predating the snapshot produced cuts: %v", cuts)
	}
}

func TestWireCuts_ReportsEverySurfaceItCanDiff(t *testing.T) {
	states := []wireSurfaceState{
		{surface: wireSurfaces[0], present: true, prev: shapesBefore, cur: `{"types":[{"type":"grant","fields":[]}]}`},
		{
			surface: wireSurfaces[2],
			present: true,
			prev:    "const ProtocolMajor = 3\nconst MinProtocolMajor = 1\n",
			cur:     "const ProtocolMajor = 4\nconst MinProtocolMajor = 2\n",
		},
	}
	cuts, err := wireCuts(states)
	if err != nil {
		t.Fatalf("wireCuts: %v", err)
	}
	joined := strings.Join(cuts, "\n")
	for _, want := range []string{"wingwire grant.run_id removed", "wingwire stats_reset removed", "protocol floor raised 1 -> 2"} {
		if !strings.Contains(joined, want) {
			t.Errorf("cuts %v omit %q", cuts, want)
		}
	}
}
