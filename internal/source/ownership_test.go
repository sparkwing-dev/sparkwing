package source_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestGitAcquisitionHasOneOwner(t *testing.T) {
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate repository")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(here), "../.."))
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if strings.HasPrefix(entry.Name(), ".") && path != root || entry.Name() == "node_modules" || entry.Name() == "vendor" || entry.Name() == "testdata" || relative == filepath.Join("internal", "source") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		positions := token.NewFileSet()
		file, err := parser.ParseFile(positions, path, nil, 0)
		if err != nil {
			return err
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if ok && acquiresGit(function) {
				t.Errorf("%s: Git acquisition belongs to internal/source", positions.Position(function.Pos()))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func acquiresGit(function *ast.FuncDecl) bool {
	bindings := map[string][]ast.Expr{}
	ast.Inspect(function, func(node ast.Node) bool {
		if assign, ok := node.(*ast.AssignStmt); ok {
			for i, expr := range assign.Lhs {
				if id, ok := expr.(*ast.Ident); ok && i < len(assign.Rhs) {
					bindings[id.Name] = append(bindings[id.Name], assign.Rhs[i])
				}
			}
		}
		if declaration, ok := node.(*ast.ValueSpec); ok {
			for i, id := range declaration.Names {
				if i < len(declaration.Values) {
					bindings[id.Name] = append(bindings[id.Name], declaration.Values[i])
				}
			}
		}
		return true
	})
	var argument func(ast.Expr, int) bool
	argument = func(expr ast.Expr, depth int) bool {
		if depth > 8 {
			return false
		}
		switch expr := expr.(type) {
		case *ast.BasicLit:
			if expr.Kind != token.STRING {
				return false
			}
			value, _ := strconv.Unquote(expr.Value)
			return value == "clone" || value == "checkout" || value == "worktree" || strings.Contains(value, "git clone ") || strings.Contains(value, "git checkout ") || strings.Contains(value, "git worktree ")
		case *ast.CompositeLit:
			for _, item := range expr.Elts {
				if argument(item, depth+1) {
					return true
				}
			}
		case *ast.Ident:
			for _, value := range bindings[expr.Name] {
				if argument(value, depth+1) {
					return true
				}
			}
		case *ast.CallExpr:
			if id, ok := expr.Fun.(*ast.Ident); ok && id.Name == "append" {
				for _, arg := range expr.Args {
					if argument(arg, depth+1) {
						return true
					}
				}
			}
		}
		return false
	}
	found := false
	ast.Inspect(function, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if localMetadataClone(call) {
			return true
		}
		name := ""
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			name = fun.Name
		case *ast.SelectorExpr:
			name = fun.Sel.Name
		}
		if name == "Command" || name == "CommandContext" || name == "git" || name == "read" || strings.HasPrefix(name, "runGit") {
			for _, arg := range call.Args {
				found = found || argument(arg, 0)
			}
		}
		return true
	})
	return found
}

func localMetadataClone(call *ast.CallExpr) bool {
	function, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || function.Sel.Name != "CommandContext" || len(call.Args) != 9 || call.Ellipsis.IsValid() {
		return false
	}
	owner, ok := function.X.(*ast.Ident)
	if !ok || owner.Name != "exec" {
		return false
	}
	// safety: git ignores --local for URLs, so the one caller also restricts the clone to the file protocol.
	for i, want := range []string{"git", "clone", "--local", "--shared", "--no-checkout", "--"} {
		literal, ok := call.Args[i+1].(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return false
		}
		got, err := strconv.Unquote(literal.Value)
		if err != nil || got != want {
			return false
		}
	}
	return true
}

func TestGitAcquisitionGuardRecognizesCommandArguments(t *testing.T) {
	for _, body := range []string{
		`exec.Command("git", "clone", remote)`,
		`args:=[]string{"checkout", "HEAD"};exec.CommandContext(ctx,"git",args...)`,
		`args:=[]string{"clone"};args=append(args,remote);runGitEnv(ctx,dir,env,args...)`,
		`runGitIn(repo,nil,"worktree","add",path)`,
		`exec.Command("sh","-c","git clone remote")`,
		`exec.CommandContext(ctx,"git","clone","--shared","--no-checkout","--",remote,destination)`,
		`exec.CommandContext(ctx,"git","clone","--local","--shared","--",source,destination)`,
		`exec.CommandContext(ctx,"git","clone","--local","--shared","--no-checkout","--branch","main","--",source,destination)`,
	} {
		file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", "package fixture;func f(){"+body+"}", 0)
		if err != nil {
			t.Fatal(err)
		}
		if !acquiresGit(file.Decls[0].(*ast.FuncDecl)) {
			t.Fatalf("missed acquisition: %s", body)
		}
	}
	for _, body := range []string{
		`path:=filepath.Join(root,"worktree");runGitIn(path,nil,"fetch")`,
		`exec.CommandContext(ctx,"git","clone","--local","--shared","--no-checkout","--",source,destination)`,
	} {
		file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", "package fixture;func f(){"+body+"}", 0)
		if err != nil {
			t.Fatal(err)
		}
		if acquiresGit(file.Decls[0].(*ast.FuncDecl)) {
			t.Fatalf("local metadata classified as Git acquisition: %s", body)
		}
	}
}
