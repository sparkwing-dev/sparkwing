package acceptance_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
)

func TestPortInventory(t *testing.T) {
	body, err := os.ReadFile("../../design/rewrite/contracts/t1-port-inventory.md")
	if err != nil {
		t.Fatal(err)
	}
	const begin = "<!-- port-inventory:begin -->"
	const end = "<!-- port-inventory:end -->"
	if strings.Count(string(body), begin) != 1 || strings.Count(string(body), end) != 1 {
		t.Fatal("inventory requires one delimited function table")
	}
	_, table, _ := strings.Cut(string(body), begin)
	table, _, found := strings.Cut(table, end)
	if !found {
		t.Fatal("inventory end precedes beginning")
	}
	lines := strings.Split(strings.TrimSpace(table), "\n")
	if len(lines) < 3 || lines[0] != "| Branch label | Revision | Source file | Source function | Cases | Disposition | New home and expected behavior |" || lines[1] != "|---|---|---|---|---|---|---|" {
		t.Fatal("malformed inventory header")
	}
	counts := map[string]int{}
	seen := map[string]bool{}
	source := regexp.MustCompile("^`[a-z0-9]+(?:-[a-z0-9]+)*`$")
	revision := regexp.MustCompile("^`[0-9a-f]{40}`$")
	file := regexp.MustCompile("^`[a-zA-Z0-9_/]+_test\\.go`$")
	function := regexp.MustCompile("^`Test[A-Za-z0-9_]+`$")
	for number, line := range lines[2:] {
		cells := strings.Split(line, "|")
		if len(cells) != 9 || cells[0] != "" || cells[8] != "" {
			t.Fatalf("malformed inventory row %d: %q", number+1, line)
		}
		for i := 1; i <= 7; i++ {
			cells[i] = strings.TrimSpace(cells[i])
			if cells[i] == "" {
				t.Fatalf("empty inventory cell at row %d column %d", number+1, i)
			}
		}
		if !source.MatchString(cells[1]) || !revision.MatchString(cells[2]) || !file.MatchString(cells[3]) || !function.MatchString(cells[4]) {
			t.Fatalf("malformed source identity at row %d: %q", number+1, line)
		}
		for _, disposition := range strings.Split(cells[6], "/") {
			switch disposition {
			case "Keep", "Reverse", "Replace", "Drop":
			default:
				t.Fatalf("unknown disposition %q", disposition)
			}
		}
		identity := strings.Join(cells[2:5], ":")
		if seen[identity] {
			t.Fatalf("duplicate source identity %s", identity)
		}
		seen[identity] = true
		counts[strings.Trim(cells[1], "`")]++
		if validatePortReferences(t, cells[7]) == 0 {
			t.Errorf("source %s has no destination", identity)
		}
	}
	want := map[string]int{"node-lifecycle": 6, "node-state-machine": 6, "placement": 14, "credential-matrix": 1, "run-input-divergences": 26, "storage-hotfixes": 26, "design-found-hotfixes": 11}
	for branch, count := range want {
		if counts[branch] != count {
			t.Errorf("branch %s has %d functions, want %d", branch, counts[branch], count)
		}
		delete(counts, branch)
	}
	for branch := range counts {
		t.Errorf("unknown source branch %s", branch)
	}
}

func validatePortReferences(t *testing.T, body string) int {
	t.Helper()
	scenarios := map[string]acceptance.Scenario{}
	for _, scenario := range acceptance.Scenarios() {
		scenarios[scenario.ID] = scenario
	}
	code := regexp.MustCompile("`([^`]+)`")
	destination := regexp.MustCompile(`^([A-Za-z0-9_/]+_test\.go):(Test[A-Za-z0-9_]+)(?:/([a-z0-9-]+)(?:/[^\x60]+)?)?$`)
	count := 0
	for _, match := range code.FindAllStringSubmatch(body, -1) {
		value := match[1]
		if !strings.Contains(value, "_test.go:") {
			continue
		}
		count++
		parts := destination.FindStringSubmatch(value)
		if parts == nil {
			t.Errorf("malformed port destination %q", value)
			continue
		}
		if strings.Contains(parts[1], "/") {
			continue
		}
		scenario, exists := scenarios[parts[3]]
		group := strings.TrimSuffix(parts[1], "_test.go")
		if !exists || scenario.Group != group {
			t.Errorf("port destination %q has no matching scenario group", value)
		}
		if err := destinationFunction(parts[1], parts[2]); err != nil {
			t.Error(err)
		}
	}
	if strings.Count(body, "_test.go:") != count {
		t.Error("port destination is missing or has malformed code delimiters")
	}
	return count
}

func destinationFunction(path, name string) error {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		return err
	}
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != name || function.Recv != nil {
			continue
		}
		params := function.Type.Params.List
		if len(params) != 1 || len(params[0].Names) > 1 || function.Type.Results != nil || function.Type.TypeParams != nil {
			break
		}
		pointer, ok := params[0].Type.(*ast.StarExpr)
		if !ok {
			break
		}
		selector, ok := pointer.X.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "T" {
			break
		}
		qualifier, ok := selector.X.(*ast.Ident)
		if !ok || qualifier.Name != "testing" {
			break
		}
		return nil
	}
	return fmt.Errorf("port destination %s:%s has no matching Go test function", path, name)
}

func TestPortDestinationFunction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "example_test.go")
	for _, test := range []struct {
		name, declaration string
		valid             bool
	}{
		{"exact function", "func TestExpected(t *testing.T) {}", true},
		{"different function", "func TestOther(t *testing.T) {}", false},
		{"comment only", "// func TestExpected(t *testing.T) {}", false},
		{"wrong signature", "func TestExpected() {}", false},
		{"method", "type suite struct{}; func (suite) TestExpected(t *testing.T) {}", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte("package acceptance_test\nimport \"testing\"\n"+test.declaration), 0o600); err != nil {
				t.Fatal(err)
			}
			err := destinationFunction(path, "TestExpected")
			if (err == nil) != test.valid {
				t.Fatalf("destination validation returned %v; valid = %v", err, test.valid)
			}
		})
	}
}
