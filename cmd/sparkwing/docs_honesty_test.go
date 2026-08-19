package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/docs"
)

// A repo doc outside the embedded set gets the same treatment through these
// markers: text between them describes intent, text outside it describes
// shipped behavior. The exclusion is written down and checked rather than
// implied by nothing looking at the file, which is how a README comes to
// document verbs the binary answers with `unknown subcommand`.
const (
	designRegionOpen  = "<!-- design:begin -->"
	designRegionClose = "<!-- design:end -->"
)

// repoDocs are the markdown files outside the embedded set that the honesty
// check still reads. README.md is the doc a stranger reads first, so a command
// name in it is as much a promise as one on a page.
var repoDocs = []string{"../../README.md"}

// docCommand matches a `sparkwing <verb> [<verb>...]` invocation and captures
// the run of lowercase words after the binary name. It is anchored, and is
// applied only to the runnable units codeUnits produces, because a command
// line is something a doc puts at the front of one -- "helm install sparkwing
// charts/sparkwing-full" names a helm release, and "1. sparkwing resolves the
// profile" is a sentence in a flow diagram.
//
// The trailing \b on each word is what keeps `sparkwing v0.16.0` -- a release
// name, which the pages use constantly -- from reading as a call to a verb `v`.
var docCommand = regexp.MustCompile(`^sparkwing((?: [a-z][a-z-]*\b)+)`)

// intentPages carry verbs the binary does not have. Rule 3 of the
// docs-publishing card puts declared-but-unbuilt surface on a page named as
// intent and nowhere else in the set, and these are those pages: everything
// under proposals/ is a proposal, and mcp.md opens by saying the server is not
// implemented. TestIntentPagesDeclareThatTheyAreNotShipped holds them to
// saying so, so this exemption cannot spread to a page that reads as
// reference.
var intentPages = []string{"mcp", "proposals/"}

// versionedPages describe binaries other than the one they ship in. The
// changelog records what each release did and the migration notes tell a
// reader what the version they are leaving accepted, so a verb that was
// renamed three releases ago belongs in them exactly as it was spelled then.
// Checking these against today's dispatch would demand that history be
// rewritten to match the present, which is the opposite of their purpose.
var versionedPages = []string{"changelog", "migrations/"}

func TestEmbeddedReferencePagesNameOnlyDispatchedCommands(t *testing.T) {
	dispatch := dispatchedPaths(t)
	pages := docs.List()
	if len(pages) == 0 {
		t.Fatal("no docs embedded, so this check reads nothing")
	}
	read := 0
	for _, e := range pages {
		if matchesAny(e.Slug, intentPages) || matchesAny(e.Slug, versionedPages) {
			continue
		}
		body, err := docs.ReadRaw(e.Slug)
		if err != nil {
			t.Fatalf("read %s: %v", e.Slug, err)
		}
		units := codeUnits(body)
		read += invocationCount(units)
		for _, name := range undispatched(units, dispatch) {
			t.Errorf("docs page %q invokes `%s`, which the CLI does not dispatch", e.Slug, name)
		}
	}
	if read == 0 {
		t.Fatal("the exemptions swallowed every page carrying an invocation, so this check reads nothing")
	}
}

// An exemption for a page that no longer exists is an exemption nobody is
// reading, and the next page whose slug happens to match it inherits a pass
// nobody granted.
func TestEveryPageExemptionMatchesAPageThatExists(t *testing.T) {
	for _, prefix := range append(append([]string{}, intentPages...), versionedPages...) {
		matched := false
		for _, e := range docs.List() {
			if matchesAny(e.Slug, []string{prefix}) {
				matched = true
				break
			}
		}
		if !matched {
			t.Errorf("the exemption %q matches no page in the set", prefix)
		}
	}
}

// An intent page is exempt from the dispatch check because it says it is
// describing something unbuilt. A page that stopped saying so is a reference
// page again, and this is what notices.
func TestIntentPagesDeclareThatTheyAreNotShipped(t *testing.T) {
	for _, e := range docs.List() {
		if !matchesAny(e.Slug, intentPages) {
			continue
		}
		body, err := docs.ReadRaw(e.Slug)
		if err != nil {
			t.Fatalf("read %s: %v", e.Slug, err)
		}
		if !declaresStatus(body) {
			t.Errorf("docs page %q is exempt from the dispatch check as an intent page, "+
				"but carries no status line saying what is and is not shipped", e.Slug)
		}
	}
}

// matchesAny reports whether slug is one of these entries or sits under one of
// them, so an entry ending in / names a directory of pages.
func matchesAny(slug string, entries []string) bool {
	for _, e := range entries {
		if slug == e || (strings.HasSuffix(e, "/") && strings.HasPrefix(slug, e)) {
			return true
		}
	}
	return false
}

// declaresStatus reports whether one of a page's opening lines is a status
// declaration, in any of the spellings the set uses: a "Status:" paragraph, a
// bolded or blockquoted one, or a "## Status" heading.
func declaresStatus(body string) bool {
	lines := strings.Split(body, "\n")
	if len(lines) > 14 {
		lines = lines[:14]
	}
	for _, line := range lines {
		trimmed := strings.ToLower(strings.TrimLeft(strings.TrimSpace(line), ">*# "))
		if strings.HasPrefix(trimmed, "status") {
			return true
		}
	}
	return false
}

func TestRepoDocsNameOnlyDispatchedCommandsOutsideTheirDesignRegions(t *testing.T) {
	dispatch := dispatchedPaths(t)
	for _, path := range repoDocs {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		shipped, err := shippedProse(string(body))
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		units := codeUnits(shipped)
		if invocationCount(units) == 0 {
			t.Fatalf("%s: no invocations outside its design regions, so this check reads nothing", path)
		}
		for _, name := range undispatched(units, dispatch) {
			t.Errorf("%s invokes `%s` outside a design region, which the CLI does not dispatch",
				path, name)
		}
	}
}

// An unbalanced marker would silently widen the exclusion until the check reads
// nothing, so shippedProse refuses the file rather than passing it.
func TestShippedProseDropsDesignRegionsAndRefusesUnbalancedMarkers(t *testing.T) {
	const begin, end = designRegionOpen, designRegionClose
	cases := []struct {
		name    string
		md      string
		want    string
		wantErr bool
	}{
		{"no markers keeps everything", "`sparkwing run`", "`sparkwing run`", false},
		{"region is dropped", "a\n" + begin + "\n`sparkwing fly`\n" + end + "\nb", "a\nb", false},
		{"two regions are dropped", begin + "\nx\n" + end + "\nkeep\n" + begin + "\ny\n" + end, "keep", false},
		{"unclosed region is refused", "a\n" + begin + "\n`sparkwing fly`", "", true},
		{"close without open is refused", "a\n" + end + "\nb", "", true},
		{"nested open is refused", begin + "\n" + begin + "\n" + end, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := shippedProse(tc.md)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("got %q, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// The pages that ship are honest, so the checks above cannot fail today. These
// cases pin the detection itself: a gate that has never been shown to bite is
// a gate nobody should trust.
func TestHonestyCheckReadsInvocationsAndIgnoresProse(t *testing.T) {
	dispatch := map[string]bool{
		"sparkwing run":       true,
		"sparkwing docs":      true,
		"sparkwing docs read": true,
	}
	cases := []struct {
		name string
		md   string
		want []string
	}{
		{"fenced invocation of a real verb", "```sh\nsparkwing run build\n```", nil},
		{"fenced invocation of a missing verb", "```sh\nsparkwing teleport\n```", []string{"sparkwing teleport"}},
		{"unlabelled fence is read as commands", "```\nsparkwing teleport\n```", []string{"sparkwing teleport"}},
		{"inline invocation", "Run `sparkwing teleport` first.", []string{"sparkwing teleport"}},
		{"a subcommand resolves to its parent path", "`sparkwing docs read --topic mcp`", nil},
		{"arguments are not verbs", "`sparkwing run my-pipeline`", nil},
		{"prose mention is not an invocation", "the sparkwing dashboard shows runs", nil},
		{"heading mention is not an invocation", "## sparkwing teleport", nil},
		{"a release name is not a verb", "`sparkwing v0.16.0` shipped it", nil},
		{"hyphenated sibling binary is not this CLI", "`sparkwing-controller serve`", nil},
		{
			"a quoted command mid-sentence is an invocation",
			"```\nset one via 'sparkwing teleport --name x'.\n```",
			[]string{"sparkwing teleport"},
		},
		{
			"a numbered prose line in a fence is not a command",
			"```\n1. sparkwing teleport the profile to the controller\n```",
			nil,
		},
		{
			"the binary named as another tool's argument is not a command",
			"```sh\nhelm install sparkwing charts/sparkwing-full\n```",
			nil,
		},
		{
			"a trailing comment is not a second invocation",
			"```sh\nsparkwing run x   # which sparkwing teleport would do\n```",
			nil,
		},
		{"a shell prompt still starts a command", "```sh\n$ sparkwing teleport\n```", []string{"sparkwing teleport"}},
		{"go fence is source, not commands", "```go\nprobe := \"sparkwing teleport\"\n```", nil},
		{"yaml fence is config, not commands", "```yaml\ncmd: sparkwing teleport\n```", nil},
		{"json fence is data, not commands", "```json\n{\"cmd\": \"sparkwing teleport\"}\n```", nil},
		{
			"deduplicated and sorted",
			"`sparkwing zeta` `sparkwing alpha` `sparkwing zeta`",
			[]string{"sparkwing alpha", "sparkwing zeta"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := undispatched(codeUnits(tc.md), dispatch)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// invocationCount reports how many units read as a call to this CLI. It backs
// the guard that a doc the check reads has something in it to read.
func invocationCount(units []string) int {
	n := 0
	for _, unit := range units {
		if docCommand.MatchString(unit) {
			n++
		}
	}
	return n
}

// The command registry is what the checks above read, so a path sitting in it
// that no top-level case dispatches would let a doc name a verb the binary
// refuses and still pass. This is the join between the two.
func TestEveryRegistryTopLevelVerbIsDispatched(t *testing.T) {
	switchCases := topLevelSwitchCases(t)
	for _, c := range allCommands {
		words := strings.Fields(c.Path)
		if len(words) < 2 {
			continue
		}
		if !switchCases[words[1]] {
			t.Errorf("the registry declares %q but runSparkwing has no case %q",
				c.Path, words[1])
		}
	}
}

// undispatched returns the invocations among these units that resolve to no
// registered command, deduplicated and sorted. An invocation resolves against
// the longest registered path that prefixes it, so `sparkwing run my-pipeline`
// is a call to `sparkwing run` carrying an argument rather than a call to a
// verb `my-pipeline`.
func undispatched(units []string, dispatch map[string]bool) []string {
	seen := map[string]bool{}
	var out []string
	for _, unit := range units {
		m := docCommand.FindStringSubmatch(unit)
		if m == nil {
			continue
		}
		words := strings.Fields(m[1])
		resolved := false
		for n := len(words); n >= 1 && !resolved; n-- {
			resolved = dispatch["sparkwing "+strings.Join(words[:n], " ")]
		}
		if resolved {
			continue
		}
		name := "sparkwing " + words[0]
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// shippedProse returns md with its design regions removed, leaving the part of
// the doc that presents itself as describing the binary as it ships. An
// unbalanced marker is an error rather than a silently wider exclusion.
func shippedProse(md string) (string, error) {
	var kept []string
	inDesign := false
	for _, line := range strings.Split(md, "\n") {
		switch strings.TrimSpace(line) {
		case designRegionOpen:
			if inDesign {
				return "", fmt.Errorf("%s opened inside a design region already open", designRegionOpen)
			}
			inDesign = true
			continue
		case designRegionClose:
			if !inDesign {
				return "", fmt.Errorf("%s with no design region open", designRegionClose)
			}
			inDesign = false
			continue
		}
		if !inDesign {
			kept = append(kept, line)
		}
	}
	if inDesign {
		return "", fmt.Errorf("%s is never closed by %s", designRegionOpen, designRegionClose)
	}
	return strings.Join(kept, "\n"), nil
}

// codeUnits returns the places in a markdown doc where something is presented
// as runnable: each line of a shell fence, and the body of each inline code
// span. Each returned unit is a place a command line can begin, so an anchored
// pattern over them reads instructions and ignores prose.
//
// Two narrowings make that true, and both are sparkwing's, not the copy
// source's.
//
// The first is on fences. The upstream copy reads every fence whatever its
// language; a Go string literal is not an imperative, and the embedded set
// carries 139 Go fences and 60 YAML ones, so reading source fences would make
// the check demand a dispatch for a word inside a struct tag. An unlabelled
// fence is still read as commands, so the default stays strict and the
// narrowing only ever drops a fence that declares itself something else.
//
// The second is that a quoted run inside a unit is itself a unit. Generated
// references can quote commands mid-sentence, and those are as much an
// instruction as a line in a fence.
func codeUnits(md string) []string {
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		s = strings.TrimPrefix(s, "$ ")
		s = strings.TrimPrefix(s, "% ")
		if s == "" {
			return
		}
		out = append(out, s)
		for _, q := range []string{"'", `"`} {
			parts := strings.Split(s, q)
			for i := 1; i < len(parts); i++ {
				if trimmed := strings.TrimSpace(parts[i]); trimmed != "" {
					out = append(out, trimmed)
				}
			}
		}
	}

	inFence, fenceIsShell := false, false
	for _, line := range strings.Split(md, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if inFence {
				inFence = false
				continue
			}
			inFence, fenceIsShell = true, shellFence(trimmed[3:])
			continue
		}
		if inFence {
			if fenceIsShell {
				add(line)
			}
			continue
		}
		spans := strings.Split(line, "`")
		for i := 1; i < len(spans); i += 2 {
			add(spans[i])
		}
	}
	return out
}

// shellFence reports whether a fence's info string means "these are command
// lines". An unlabelled fence counts, so the default stays strict.
func shellFence(info string) bool {
	switch strings.ToLower(strings.TrimSpace(info)) {
	case "", "sh", "bash", "zsh", "shell", "console", "terminal":
		return true
	}
	return false
}

// dispatchedPaths returns every invocation the binary answers, from both of
// the places that decide it.
//
// The registry carries nested paths and is the binary's own account of its
// surface: `sparkwing commands`
// serves it, the help renderer reads it, and TestAllCommandsAreRegistered
// holds it level with help_registry.go. It is not the whole answer, because
// runSparkwing also branches on verbs the registry does not carry:
// `run-node` and `handle-trigger` are dispatched for other processes to call,
// and a doc that names one is describing something real.
func dispatchedPaths(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, c := range allCommands {
		out[c.Path] = true
	}
	for verb := range topLevelSwitchCases(t) {
		out["sparkwing "+verb] = true
	}
	if len(out) < 2 {
		t.Fatal("the command registry is empty, so this check proves nothing")
	}
	return out
}

// topLevelSwitchCases reads the case values of runSparkwing's `switch args[0]`,
// which is what the process actually branches on.
func topLevelSwitchCases(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}
	out := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "runSparkwing" {
			return true
		}
		ast.Inspect(fn, func(inner ast.Node) bool {
			cc, ok := inner.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, e := range cc.List {
				lit, ok := e.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				if v, err := strconv.Unquote(lit.Value); err == nil && !strings.HasPrefix(v, "-") {
					out[v] = true
				}
			}
			return true
		})
		return false
	})
	if len(out) == 0 {
		t.Fatal("no case values found in runSparkwing, so this check proves nothing")
	}
	return out
}
