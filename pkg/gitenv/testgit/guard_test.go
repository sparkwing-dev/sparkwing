package testgit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func unsafeGitCalls(source string) ([]int, error) {
	positions := token.NewFileSet()
	file, err := parser.ParseFile(positions, "fixture.go", source, 0)
	if err != nil {
		return nil, err
	}
	constructors := map[string]bool{}
	var lines []int
	for _, imp := range file.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		if path != "os/exec" {
			continue
		}
		alias := "exec"
		if imp.Name != nil {
			alias = imp.Name.Name
		}
		if alias == "." {
			lines = append(lines, positions.Position(imp.Pos()).Line)
		}
		constructors[alias] = true
	}
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		owner, ok := selector.X.(*ast.Ident)
		if !ok || !constructors[owner.Name] {
			return true
		}
		arg := 0
		switch selector.Sel.Name {
		case "Command":
		case "CommandContext":
			arg = 1
		default:
			return true
		}
		if arg >= len(call.Args) {
			return true
		}
		git := false
		ast.Inspect(call.Args[arg], func(n ast.Node) bool {
			switch value := n.(type) {
			case *ast.BasicLit:
				name, _ := strconv.Unquote(value.Value)
				name = strings.ReplaceAll(name, "\\", "/")
				git = git || strings.EqualFold(filepath.Base(name), "git") || strings.EqualFold(filepath.Base(name), "git.exe")
			case *ast.Ident:
				git = git || strings.Contains(strings.ToLower(value.Name), "git")
			}
			return true
		})
		if git {
			lines = append(lines, positions.Position(call.Pos()).Line)
		}
		return true
	})
	return lines, nil
}

func TestGuardRejectsRawGitConstructors(t *testing.T) {
	for _, body := range []string{
		`import "os/exec"; func f(){exec.Command("git", "init")}`,
		`import run "os/exec"; func f(){run.CommandContext(ctx, "git", "init")}`,
		`import "os/exec"; func f(){exec.Command(gitBin, "init")}`,
		`import "os/exec"; func f(){exec.Command("/usr/bin/git", "init")}`,
		`import "os/exec"; func f(){exec.Command("C:\\tools\\git.exe", "init")}`,
		`import . "os/exec"; func f(){Command("git", "init")}`,
	} {
		lines, err := unsafeGitCalls("package fixture;" + body)
		if err != nil || len(lines) != 1 {
			t.Fatalf("guard(%s) = %v, %v", body, lines, err)
		}
	}
	lines, err := unsafeGitCalls("package fixture;" + `import "os/exec"; func f(){exec.Command("go", "test");testgit.Command("git","init");_="exec.Command(\"git\")"}`)
	if err != nil || len(lines) != 0 {
		t.Fatalf("safe fixture = %v, %v", lines, err)
	}
}

func TestRepositoryGitFixturesUseTheScrubbedEnvironment(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	shellGit := regexp.MustCompile(`(?m)(^|[\s$(|])git[\s"]`)
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "vendor", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		name := entry.Name()
		if !strings.HasSuffix(name, "_test.go") && !strings.HasSuffix(name, "-test.sh") {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.HasSuffix(name, ".sh") {
			firstGit := shellGit.FindIndex(source)
			scrub := strings.Index(string(source), `source "$(dirname "${BASH_SOURCE[0]}")/test-git-env.sh"`)
			if firstGit != nil && (scrub < 0 || scrub > firstGit[0]) {
				t.Errorf("%s: source bin/test-git-env.sh before running Git", path)
			}
			return nil
		}
		lines, err := unsafeGitCalls(string(source))
		if err != nil {
			return err
		}
		for _, line := range lines {
			t.Errorf("%s:%d: use testgit.Command or testgit.CommandContext", path, line)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
