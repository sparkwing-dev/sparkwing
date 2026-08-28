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

const (
	designRegionOpen  = "<!-- design:begin -->"
	designRegionClose = "<!-- design:end -->"
)

var repoDocs = []string{"../../README.md"}

var docCommand = regexp.MustCompile(`^sparkwing((?: [a-z][a-z-]*\b)+)`)

var intentPages = []string{"mcp", "proposals/"}

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

func matchesAny(slug string, entries []string) bool {
	for _, e := range entries {
		if slug == e || (strings.HasSuffix(e, "/") && strings.HasPrefix(slug, e)) {
			return true
		}
	}
	return false
}

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

func invocationCount(units []string) int {
	n := 0
	for _, unit := range units {
		if docCommand.MatchString(unit) {
			n++
		}
	}
	return n
}

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

func shellFence(info string) bool {
	switch strings.ToLower(strings.TrimSpace(info)) {
	case "", "sh", "bash", "zsh", "shell", "console", "terminal":
		return true
	}
	return false
}

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
