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
	if !reflect.DeepEqual(describeCuts(cuts), want) {
		t.Errorf("cuts = %v, want %v", describeCuts(cuts), want)
	}
	if cuts[0].Identifier != "grant.resources.cores" {
		t.Errorf("identifier = %q, want the field path the changelog has to name", cuts[0].Identifier)
	}
}

const specBefore = `openapi: 3.0.3
paths:
  /api/v1/runs:
    get:
      summary: List runs.
      parameters:
        - {name: pipeline, in: query, schema: {type: string}}
        - {name: limit, in: query, schema: {type: integer}}
      responses:
        "200":
          content:
            application/json:
              schema:
                oneOf:
                  - {properties: {runs: {type: array}}}
                  - {properties: {error: {type: string}}}
    post:
      summary: Create a run.
  /api/v1/runs/{id}:
    parameters:
      - {name: id, in: path, schema: {type: string}}
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
      parameters:
        - {name: pipeline, in: query, schema: {type: string}}
        - {name: limit, in: query, schema: {type: integer}}
      responses:
        "200":
          content:
            application/json:
              schema:
                oneOf:
                  - {properties: {runs: {type: array}}}
                  - {properties: {error: {type: string}}}
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
		"api /api/v1/runs/{id} parameter path:id removed",
		"api /api/v1/runs/{id} removed",
		"api GET /api/v1/runs/{id} removed",
		"api POST /api/v1/runs removed",
		"api components.schemas.Run.properties.status removed",
	}
	if !reflect.DeepEqual(describeCuts(cuts), want) {
		t.Errorf("cuts = %v, want %v", describeCuts(cuts), want)
	}
}

func TestAPISurfaceCuts_ParameterRenamesAreCuts(t *testing.T) {
	renamed := strings.Replace(specBefore, "{name: pipeline, in: query", "{name: pipeline_name, in: query", 1)
	cuts, err := apiSurfaceCuts(specBefore, renamed)
	if err != nil {
		t.Fatalf("apiSurfaceCuts: %v", err)
	}
	if want := []string{"api GET /api/v1/runs parameter query:pipeline removed"}; !reflect.DeepEqual(describeCuts(cuts), want) {
		t.Errorf("cuts = %v, want %v", describeCuts(cuts), want)
	}

	renamedPath := strings.Replace(specBefore, "{name: id, in: path", "{name: runID, in: path", 1)
	pathCuts, err := apiSurfaceCuts(specBefore, renamedPath)
	if err != nil {
		t.Fatalf("apiSurfaceCuts: %v", err)
	}
	if len(pathCuts) == 0 {
		t.Error("renaming a path parameter read as growth")
	}
}

func TestAPISurfaceCuts_ReorderingIsNotACut(t *testing.T) {
	reorderedParams := strings.Replace(specBefore,
		"        - {name: pipeline, in: query, schema: {type: string}}\n        - {name: limit, in: query, schema: {type: integer}}",
		"        - {name: limit, in: query, schema: {type: integer}}\n        - {name: pipeline, in: query, schema: {type: string}}", 1)
	cuts, err := apiSurfaceCuts(specBefore, reorderedParams)
	if err != nil {
		t.Fatalf("apiSurfaceCuts: %v", err)
	}
	if len(cuts) != 0 {
		t.Errorf("reordered parameters read as cuts: %v", describeCuts(cuts))
	}

	reorderedOneOf := strings.Replace(specBefore,
		"                  - {properties: {runs: {type: array}}}\n                  - {properties: {error: {type: string}}}",
		"                  - {properties: {error: {type: string}}}\n                  - {properties: {runs: {type: array}}}", 1)
	oneOfCuts, err := apiSurfaceCuts(specBefore, reorderedOneOf)
	if err != nil {
		t.Fatalf("apiSurfaceCuts: %v", err)
	}
	if len(oneOfCuts) != 0 {
		t.Errorf("a reordered oneOf read as cuts: %v", describeCuts(oneOfCuts))
	}
}

func TestAPISurfaceCuts_ASpecWithoutPathsFailsTheGate(t *testing.T) {
	for _, spec := range []string{"openapi: 3.0.3\ncomponents: {}\n", "openapi: 3.0.3\npaths: {}\n"} {
		if _, err := apiSurfaceCuts(spec, specBefore); err == nil {
			t.Errorf("a previous spec with no routes passed silently: %q", spec)
		}
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
	"_unreleased.md": {Data: []byte("# Unreleased\n\n## The wire drops stats_reset\n\nThe daemon no longer serves stats_reset.\n" +
		"grant.lease_token is gone with it, and the protocol floor rises.\n\n## Something else\n\nUnrelated.\n")},
	"v0.41.0.md": {Data: []byte("# v0.41.0\n\n## The wire drops stats_reset\n\nstats_reset and POST /api/v1/runs are gone.\n")},
}

var statsResetCut = wireCut{Surface: "wingwire", Identifier: "stats_reset", Detail: "removed"}

func TestLintWireBreak_GrowthPasses(t *testing.T) {
	body := "## [Unreleased]\n\n### Added\n\n- **wingd:** A new message.\n"
	if issues := LintWireBreak(body, "v0.41.0", nil, wireMigrations); len(issues) != 0 {
		t.Fatalf("growth should pass, got %v", formatAllIssues(issues))
	}
}

func TestLintWireBreak_DeclaredCutPasses(t *testing.T) {
	body := "## [Unreleased]\n\n### Removed\n\n" +
		"- **wingd (Breaking):** The wire drops `stats_reset`. " +
		"See [migration](docs/migrations/_unreleased.md#the-wire-drops-stats_reset).\n"
	if issues := LintWireBreak(body, "v0.41.0", []wireCut{statsResetCut}, wireMigrations); len(issues) != 0 {
		t.Fatalf("declared cut should pass, got %v", formatAllIssues(issues))
	}
}

func TestLintWireBreak_MigrationSectionMayCarryTheIdentifiers(t *testing.T) {
	body := "## [Unreleased]\n\n### Removed\n\n" +
		"- **wingd (Breaking):** The daemon cuts a message and raises its floor. " +
		"See [migration](docs/migrations/_unreleased.md#the-wire-drops-stats_reset).\n"
	cuts := []wireCut{
		statsResetCut,
		{Surface: "wingwire", Identifier: "grant.lease_token", Detail: "removed"},
		{Identifier: "protocol floor", Detail: "raised 1 -> 2 (newest major 3)"},
	}
	if issues := LintWireBreak(body, "v0.41.0", cuts, wireMigrations); len(issues) != 0 {
		t.Fatalf("cuts named in the linked migration section should pass, got %v", formatAllIssues(issues))
	}
}

func TestLintWireBreak_DeclaredCutInAVersionSectionPasses(t *testing.T) {
	body := "## [v0.41.0] - 2026-09-02\n\n### Removed\n\n" +
		"- **api (Breaking):** The controller drops a route. " +
		"See [migration](docs/migrations/v0.41.0.md#the-wire-drops-stats_reset).\n"
	cuts := []wireCut{{Surface: "api", Identifier: "POST /api/v1/runs", Detail: "removed"}}
	if issues := LintWireBreak(body, "v0.41.0", cuts, wireMigrations); len(issues) != 0 {
		t.Fatalf("declared cut should pass, got %v", formatAllIssues(issues))
	}
}

func TestLintWireBreak_UndeclaredCutFails(t *testing.T) {
	body := "## [Unreleased]\n\n### Changed\n\n- **wingd:** The wire drops `stats_reset`.\n"
	issues := LintWireBreak(body, "v0.41.0", []wireCut{statsResetCut}, wireMigrations)
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

func TestLintWireBreak_AnUnrelatedBreakingEntryDoesNotCoverACut(t *testing.T) {
	body := "## [Unreleased]\n\n### Changed\n\n" +
		"- **controller (Breaking):** Four controller limits, and a way to take /metrics off the ingress. " +
		"The API server restarts after this. " +
		"See [migration](docs/migrations/_unreleased.md#something-else).\n"
	cuts := []wireCut{
		{Surface: "wingwire", Identifier: "grant.lease_token", Detail: "removed"},
		{Surface: "api", Identifier: "DELETE /api/v1/runs/{id}", Detail: "removed"},
		{Identifier: "protocol floor", Detail: "raised 1 -> 2 (newest major 3)"},
	}
	issues := LintWireBreak(body, "v0.41.0", cuts, wireMigrations)
	if len(issues) != 1 || issues[0].Category != wireCoverageCategory {
		t.Fatalf("issues = %v, want one undeclared-wire-cut: an entry about ingress limits declares no wire cut", formatAllIssues(issues))
	}
	for _, cut := range cuts {
		if !strings.Contains(issues[0].Message, cut.Identifier) {
			t.Errorf("message does not report %q as undeclared: %s", cut.Identifier, issues[0].Message)
		}
	}
}

func TestLintWireBreak_AnEntryNamingSomeCutsFailsOnTheRest(t *testing.T) {
	body := "## [Unreleased]\n\n### Removed\n\n" +
		"- **wingd (Breaking):** The wire drops `stats_reset` and `grant.lease_token`. " +
		"See [migration](docs/migrations/_unreleased.md#something-else).\n"
	cuts := []wireCut{
		statsResetCut,
		{Surface: "wingwire", Identifier: "grant.lease_token", Detail: "removed"},
		{Surface: "api", Identifier: "DELETE /api/v1/runs/{id}", Detail: "removed"},
	}
	issues := LintWireBreak(body, "v0.41.0", cuts, wireMigrations)
	if len(issues) != 1 || issues[0].Category != wireCoverageCategory {
		t.Fatalf("issues = %v, want one undeclared-wire-cut", formatAllIssues(issues))
	}
	if !strings.Contains(issues[0].Message, "DELETE /api/v1/runs/{id}") {
		t.Errorf("message does not name the undeclared cut: %s", issues[0].Message)
	}
	for _, named := range []string{"stats_reset removed;", "grant.lease_token"} {
		if strings.Contains(issues[0].Message, named) {
			t.Errorf("message reports a cut the entry does name: %s", issues[0].Message)
		}
	}
}

func TestLintWireBreak_CutWithoutAMigrationLinkFails(t *testing.T) {
	body := "## [Unreleased]\n\n### Removed\n\n- **wingd (Breaking):** The wire drops `stats_reset`.\n"
	issues := LintWireBreak(body, "v0.41.0", []wireCut{statsResetCut}, wireMigrations)
	if len(issues) != 1 || issues[0].Category != wireMigrationCategory {
		t.Fatalf("issues = %v, want one missing-wire-migration", formatAllIssues(issues))
	}
}

func TestLintWireBreak_CutLinkingAnAbsentSectionFails(t *testing.T) {
	body := "## [Unreleased]\n\n### Removed\n\n" +
		"- **wingd (Breaking):** The wire drops `stats_reset`. " +
		"See [migration](docs/migrations/_unreleased.md#no-such-heading).\n"
	issues := LintWireBreak(body, "v0.41.0", []wireCut{statsResetCut}, wireMigrations)
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
		t.Errorf("a tag predating the snapshot produced cuts: %v", describeCuts(cuts))
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
	joined := strings.Join(describeCuts(cuts), "\n")
	for _, want := range []string{"wingwire grant.run_id removed", "wingwire stats_reset removed", "protocol floor raised 1 -> 2"} {
		if !strings.Contains(joined, want) {
			t.Errorf("cuts %v omit %q", describeCuts(cuts), want)
		}
	}
}

func TestLintWireBreak_NamingTheRouteCoversEverythingUnderIt(t *testing.T) {
	removed := removeSpecPath(specBefore, "  /api/v1/runs:")
	cuts, err := apiSurfaceCuts(specBefore, removed)
	if err != nil {
		t.Fatalf("apiSurfaceCuts: %v", err)
	}
	if len(cuts) < 5 {
		t.Fatalf("removing one route produced %d cuts, want the route, its methods, parameters and members", len(cuts))
	}
	honest := "## [Unreleased]\n\n### Removed\n\n" +
		"- **api (Breaking):** The controller no longer serves `/api/v1/runs`. " +
		"See [migration](docs/migrations/_unreleased.md#something-else).\n"
	if issues := LintWireBreak(honest, "v0.41.0", cuts, wireMigrations); len(issues) != 0 {
		t.Errorf("a note naming the removed route should declare every cut under it, got %v", formatAllIssues(issues))
	}

	byMethodOnly := "## [Unreleased]\n\n### Removed\n\n" +
		"- **api (Breaking):** `GET /api/v1/runs` is gone. " +
		"See [migration](docs/migrations/_unreleased.md#something-else).\n"
	issues := LintWireBreak(byMethodOnly, "v0.41.0", cuts, wireMigrations)
	if len(issues) != 1 || issues[0].Category != wireCoverageCategory {
		t.Fatalf("issues = %v, want one undeclared-wire-cut for the route and the other method", formatAllIssues(issues))
	}
	for _, want := range []string{"/api/v1/runs removed", "POST /api/v1/runs removed"} {
		if !strings.Contains(issues[0].Message, want) {
			t.Errorf("message does not report %q: %s", want, issues[0].Message)
		}
	}
}

func removeSpecPath(spec, header string) string {
	var out []string
	skip := false
	for _, line := range strings.Split(spec, "\n") {
		if line == header {
			skip = true
			continue
		}
		if skip && strings.HasPrefix(line, "  /") {
			skip = false
		}
		if skip && !strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "      ") && !strings.HasPrefix(line, "        ") {
			skip = false
		}
		if !skip {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func TestDeclaresIdentifierMatchesOnBoundaries(t *testing.T) {
	cases := []struct {
		text       string
		identifier string
		want       bool
	}{
		{"the wire drops `hello`", "hello", true},
		{"the wire drops `hello_ack`", "hello", false},
		{"the wire drops `hello`", "hello_ack", false},
		{"the wire drops `cancel_lease`", "cancel", false},
		{"the daemon no longer serves stats_reset.", "stats_reset", true},
		{"grant.lease_token is gone", "grant", false},
		{"the `grant` message is gone", "grant.lease_token", false},
		{"`DELETE /api/v1/runs/{id}` is gone", "DELETE /api/v1/runs", false},
		{"`/api/v1/runs` is gone", "/api/v1/runs/{id}", false},
		{"`/api/v1/runs` is gone.", "/api/v1/runs", true},
		{"the protocol floor rises to 2", "protocol floor", true},
	}
	for _, tc := range cases {
		if got := declaresIdentifier(tc.text, tc.identifier); got != tc.want {
			t.Errorf("declaresIdentifier(%q, %q) = %v, want %v", tc.text, tc.identifier, got, tc.want)
		}
	}
}

func TestLintWireBreak_ANamedFieldDoesNotDeclareItsType(t *testing.T) {
	body := "## [Unreleased]\n\n### Removed\n\n" +
		"- **wingwire (Breaking):** `grant.lease_token` is gone. " +
		"See [migration](docs/migrations/_unreleased.md#something-else).\n"
	cuts := []wireCut{{
		Surface:    "wingwire",
		Identifier: "grant",
		Detail:     "removed",
		Covers:     wireFieldAncestors("grant"),
	}}
	issues := LintWireBreak(body, "v0.41.0", cuts, wireMigrations)
	if len(issues) != 1 || issues[0].Category != wireCoverageCategory {
		t.Fatalf("issues = %v, want one undeclared-wire-cut: naming a field does not declare its whole type", formatAllIssues(issues))
	}
}

func TestLintWireBreak_ANamedTypeDeclaresItsFields(t *testing.T) {
	body := "## [Unreleased]\n\n### Removed\n\n" +
		"- **wingwire (Breaking):** The `grant` message is gone. " +
		"See [migration](docs/migrations/_unreleased.md#something-else).\n"
	cuts := []wireCut{
		{Surface: "wingwire", Identifier: "grant", Detail: "removed", Covers: wireFieldAncestors("grant")},
		{Surface: "wingwire", Identifier: "grant.resources.cores", Detail: "removed", Covers: wireFieldAncestors("grant.resources.cores")},
	}
	if issues := LintWireBreak(body, "v0.41.0", cuts, wireMigrations); len(issues) != 0 {
		t.Fatalf("naming the type should declare its fields, got %v", formatAllIssues(issues))
	}
}

func TestLintWireBreak_AcceptsEveryScopeAWireCutShipsUnder(t *testing.T) {
	for _, scope := range wireScopes {
		body := "## [Unreleased]\n\n### Removed\n\n" +
			"- **" + scope + " (Breaking):** `stats_reset` is gone. " +
			"See [migration](docs/migrations/_unreleased.md#something-else).\n"
		if issues := LintWireBreak(body, "v0.41.0", []wireCut{statsResetCut}, wireMigrations); len(issues) != 0 {
			t.Errorf("scope %q should declare a wire cut, got %v", scope, formatAllIssues(issues))
		}
	}
	body := "## [Unreleased]\n\n### Removed\n\n" +
		"- **store (Breaking):** `stats_reset` is gone. " +
		"See [migration](docs/migrations/_unreleased.md#something-else).\n"
	if issues := LintWireBreak(body, "v0.41.0", []wireCut{statsResetCut}, wireMigrations); len(issues) != 1 {
		t.Errorf("a store-scoped entry should not declare a wire cut, got %v", formatAllIssues(issues))
	}
}

func TestAPISurfaceCuts_ComponentParameterRenamesAreCuts(t *testing.T) {
	spec := specBefore + `  parameters:
    RunID:
      name: id
      in: path
      schema: {type: string}
`
	renamed := strings.Replace(spec, "      name: id\n", "      name: runID\n", 1)
	cuts, err := apiSurfaceCuts(spec, renamed)
	if err != nil {
		t.Fatalf("apiSurfaceCuts: %v", err)
	}
	want := []string{"api components.parameters.RunID parameter path:id removed"}
	if !reflect.DeepEqual(describeCuts(cuts), want) {
		t.Errorf("cuts = %v, want %v", describeCuts(cuts), want)
	}
	if len(cuts) == 1 && !strings.Contains(strings.Join(cuts[0].covering(), " "), "components.parameters.RunID") {
		t.Errorf("covering = %v, want the component name to declare it", cuts[0].covering())
	}
}
