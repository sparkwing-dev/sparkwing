package fssecure_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

var configDirPackages = map[string]bool{
	"internal/fssecure": true,
	"internal/paths":    true,
	"internal/profile":  true,
	"internal/repos":    true,
	"internal/secrets":  true,
}

var configDirProviders = map[string]bool{
	"fssecure.ConfigDir":        true,
	"fssecure.ConfigDirIn":      true,
	"fssecure.ConfigFile":       true,
	"fssecure.EnsureConfigDir":  true,
	"fssecure.UnderConfigDir":   true,
	"profile.DefaultPath":       true,
	"repos.DefaultPath":         true,
	"secrets.DefaultConfigPath": true,
	"secrets.DefaultDotenvPath": true,
}

var configDirWriters = []string{
	"cmd/sparkwing/configure_init.go",
	"cmd/sparkwing/versionhold.go",
	"internal/cluster/runner_agent_cli.go",
	"internal/configguard/configguard.go",
	"internal/profile/profile.go",
	"internal/repos/repos.go",
	"internal/secrets/dotenv.go",
	"internal/wingd/budget_resolve.go",
}

type modeCall struct {
	line int
	fn   string
	perm uint64
}

// safety: a convention nudge for config-directory writers, not a boundary; an unresolvable mode passes.
func TestConfigDirectoryWritersDoNotMkdirGroupReadable(t *testing.T) {
	root := filepath.Join("..", "..")
	files, err := parseRepo(root)
	if err != nil {
		t.Fatalf("parse %s: %v", root, err)
	}
	consts := packageConsts(files)

	scoped := map[string]bool{}
	for _, f := range files {
		if !configDirPackages[filepath.ToSlash(filepath.Dir(f.rel))] && !resolvesConfigDir(f.file) {
			continue
		}
		scoped[f.rel] = true
		for _, call := range looseModeCalls(f, consts[filepath.Dir(f.rel)]) {
			t.Errorf("%s:%d: %s with mode %04o leaves the sparkwing config directory reachable by group or "+
				"other users; use the fssecure helpers instead", f.rel, call.line, call.fn, call.perm)
		}
	}
	if len(files) == 0 || len(scoped) == 0 {
		t.Fatalf("scanned %d files and matched %d config-directory writers, so this check proves nothing", len(files), len(scoped))
	}
	for _, want := range configDirWriters {
		if !scoped[want] {
			t.Errorf("%s builds a path in the sparkwing config directory but the check does not cover it", want)
		}
	}
}

func TestLooseModeCallsFindsEverySpelling(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want bool
	}{
		{"bare literal", "func f() { _ = os.MkdirAll(p, 0o755) }", true},
		{"named const", "const dirMode = 0o755\nfunc f() { _ = os.MkdirAll(p, dirMode) }", true},
		{"file mode conversion", "func f() { _ = os.MkdirAll(p, fs.FileMode(0o750)) }", true},
		{"aliased import", "func f() { _ = goos.MkdirAll(p, 0o755) }", true},
		{"arithmetic", "func f() { _ = os.MkdirAll(p, 0o700|0o055) }", true},
		{"write file", "func f() { _ = os.WriteFile(p, b, 0o644) }", true},
		{"open file", "func f() { _, _ = os.OpenFile(p, flag, 0o640) }", true},
		{"widening chmod", "func f() { _ = os.Chmod(p, 0o755) }", true},
		{"private mkdir", "func f() { _ = os.MkdirAll(p, 0o700) }", false},
		{"private write", "func f() { _ = os.WriteFile(p, b, 0o600) }", false},
		{"unresolved mode", "func f() { _ = os.MkdirAll(p, mode) }", false},
		{"other package", "func f() { _ = unix.Mkdir(p, 0o755) }", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			imports := "import (\n\t\"io/fs\"\n\tgoos \"os\"\n)\n"
			if !strings.Contains(tc.src, "goos.") {
				imports = "import (\n\t\"io/fs\"\n\t\"os\"\n)\n"
			}
			src := "package p\n" + imports + tc.src
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "probe.go", src, 0)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			parsed := parsedFile{rel: "probe.go", file: file, fset: fset, osName: osImportName(file)}
			got := looseModeCalls(parsed, fileConsts(file))
			if (len(got) > 0) != tc.want {
				t.Errorf("looseModeCalls = %v, want flagged=%v", got, tc.want)
			}
		})
	}
}

type parsedFile struct {
	rel    string
	file   *ast.File
	fset   *token.FileSet
	osName string
}

func parseRepo(root string) ([]parsedFile, error) {
	var out []parsedFile
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if path != root && skipScanDir(path, d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		out = append(out, parsedFile{rel: filepath.ToSlash(rel), file: file, fset: fset, osName: osImportName(file)})
		return nil
	})
	return out, err
}

func skipScanDir(path, name string) bool {
	switch name {
	case "node_modules", "vendor", "testdata":
		return true
	}
	if strings.HasPrefix(name, ".") {
		return true
	}
	_, err := os.Stat(filepath.Join(path, "go.mod"))
	return err == nil
}

func packageConsts(files []parsedFile) map[string]map[string]uint64 {
	out := map[string]map[string]uint64{}
	for _, f := range files {
		dir := filepath.Dir(f.rel)
		if out[dir] == nil {
			out[dir] = map[string]uint64{}
		}
		for name, value := range fileConsts(f.file) {
			out[dir][name] = value
		}
	}
	return out
}

func fileConsts(file *ast.File) map[string]uint64 {
	out := map[string]uint64{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || (gen.Tok != token.CONST && gen.Tok != token.VAR) {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range value.Names {
				if i >= len(value.Values) {
					continue
				}
				if perm, ok := literalPerm(value.Values[i], nil); ok {
					out[name.Name] = perm
				}
			}
		}
	}
	return out
}

func osImportName(file *ast.File) string {
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || path != "os" {
			continue
		}
		if spec.Name != nil {
			return spec.Name.Name
		}
		return "os"
	}
	return ""
}

func resolvesConfigDir(file *ast.File) bool {
	found, dotConfig, sparkwing := false, false, false
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.SelectorExpr:
			pkg, ok := node.X.(*ast.Ident)
			if ok && configDirProviders[pkg.Name+"."+node.Sel.Name] {
				found = true
			}
		case *ast.BasicLit:
			if node.Kind != token.STRING {
				return true
			}
			switch value, err := strconv.Unquote(node.Value); {
			case err != nil:
			case value == "XDG_CONFIG_HOME":
				found = true
			case value == ".config":
				dotConfig = true
			case value == "sparkwing":
				sparkwing = true
			}
		}
		return !found
	})
	return found || (dotConfig && sparkwing)
}

var modePositions = map[string]int{
	"Chmod":     1,
	"Mkdir":     1,
	"MkdirAll":  1,
	"OpenFile":  2,
	"WriteFile": 2,
}

func looseModeCalls(f parsedFile, consts map[string]uint64) []modeCall {
	if f.osName == "" {
		return nil
	}
	var out []modeCall
	ast.Inspect(f.file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != f.osName {
			return true
		}
		at, ok := modePositions[sel.Sel.Name]
		if !ok || len(call.Args) != at+1 {
			return true
		}
		perm, ok := literalPerm(call.Args[at], consts)
		if !ok || perm&0o077 == 0 {
			return true
		}
		out = append(out, modeCall{
			line: f.fset.Position(call.Pos()).Line,
			fn:   f.osName + "." + sel.Sel.Name,
			perm: perm,
		})
		return true
	})
	return out
}

func literalPerm(arg ast.Expr, consts map[string]uint64) (uint64, bool) {
	switch node := arg.(type) {
	case *ast.BasicLit:
		if node.Kind != token.INT {
			return 0, false
		}
		value, err := strconv.ParseUint(node.Value, 0, 32)
		if err != nil {
			return 0, false
		}
		return value, true
	case *ast.Ident:
		value, ok := consts[node.Name]
		return value, ok
	case *ast.ParenExpr:
		return literalPerm(node.X, consts)
	case *ast.BinaryExpr:
		left, leftOK := literalPerm(node.X, consts)
		right, rightOK := literalPerm(node.Y, consts)
		if !leftOK || !rightOK {
			return 0, false
		}
		switch node.Op {
		case token.OR:
			return left | right, true
		case token.AND:
			return left & right, true
		}
		return 0, false
	case *ast.CallExpr:
		if len(node.Args) != 1 || !isFileModeConversion(node.Fun) {
			return 0, false
		}
		return literalPerm(node.Args[0], consts)
	}
	return 0, false
}

func isFileModeConversion(fun ast.Expr) bool {
	switch node := fun.(type) {
	case *ast.Ident:
		return node.Name == "FileMode"
	case *ast.SelectorExpr:
		return node.Sel.Name == "FileMode"
	}
	return false
}
