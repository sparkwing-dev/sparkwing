package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/docs"
)

const envPrefix = "SPARKWING_"

func docsMentionEnvVar(documented, name string) bool {
	for from := 0; from <= len(documented)-len(name); {
		rel := strings.Index(documented[from:], name)
		if rel < 0 {
			return false
		}
		start := from + rel
		end := start + len(name)
		leftBoundary := start == 0 || !isIdentifierByte(documented[start-1])
		rightBoundary := end == len(documented) || !isIdentifierByte(documented[end])
		if leftBoundary && rightBoundary {
			return true
		}
		from = start + 1
	}
	return false
}

func isIdentifierByte(c byte) bool {
	return c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
}

func TestDocsMentionEnvVarDoesNotAllocatePerLookup(t *testing.T) {
	mentioned := false
	allocs := testing.AllocsPerRun(100, func() {
		mentioned = docsMentionEnvVar("before SPARKWING_EXAMPLE_TOKEN after", "SPARKWING_EXAMPLE_TOKEN")
	})
	if !mentioned {
		t.Fatal("environment variable token was not found")
	}
	if allocs != 0 {
		t.Fatalf("docsMentionEnvVar allocated %.0f objects per lookup, want 0", allocs)
	}
}

func TestDocsMentionEnvVarRequiresWholeIdentifierToken(t *testing.T) {
	const name = "SPARKWING_EXAMPLE_CACHE"
	for _, tc := range []struct {
		documented string
		want       bool
	}{
		{name, true},
		{"before " + name, true},
		{name + " after", true},
		{"`" + name + "`", true},
		{name + "_URL", false},
		{"MY_" + name, false},
		{"before " + name + "_URL then " + name, true},
	} {
		if got := docsMentionEnvVar(tc.documented, name); got != tc.want {
			t.Errorf("docsMentionEnvVar(%q, %q) = %t, want %t", tc.documented, name, got, tc.want)
		}
	}
}

var undocumentedEnvVars = []string{
	"SPARKWING_AUTO_REGISTER_WORKTREES",
	"SPARKWING_BAKED_BINARY",
	"SPARKWING_BINARY_SOURCE",
	"SPARKWING_BROKERED_ARTIFACTS",
	"SPARKWING_BROKERED_NODE_CLAIM",
	"SPARKWING_CHAOS_KEEP",
	"SPARKWING_CHILD_LEASE_TOKEN",
	"SPARKWING_DEBUG_PAUSE_AFTER",
	"SPARKWING_DEBUG_PAUSE_BEFORE",
	"SPARKWING_DEBUG_PAUSE_ON_FAILURE",
	"SPARKWING_DISPATCH_WAIT_TIMEOUT",
	"SPARKWING_DOCS_BASE_URL",
	"SPARKWING_EXECUTION_CAPABILITY_STDIN",
	"SPARKWING_FORCE_COLOR",
	"SPARKWING_GITCACHE_CONCURRENCY",
	"SPARKWING_IMAGE_PULL_SECRET",
	"SPARKWING_LEASE_TOKEN",
	"SPARKWING_LOCAL_ONLY",
	"SPARKWING_LOG_LEVEL",
	"SPARKWING_NAMESPACE",
	"SPARKWING_NO_CACHE",
	"SPARKWING_NO_UPDATE",
	"SPARKWING_ONLY",
	"SPARKWING_PROFILE",
	"SPARKWING_REF",
	"SPARKWING_NODE_CLAIM_GENERATION",
	"SPARKWING_NODE_CLAIM_HOLDER",
	"SPARKWING_NODE_CLAIM_MEMBERSHIP",
	"SPARKWING_NODE_CLAIM_RESERVATION",
	"SPARKWING_REPOS",
	"SPARKWING_RUNNER_CONTROLLER_URL",
	"SPARKWING_RUNNER_IMAGE",
	"SPARKWING_RUNNER_LOGS_URL",
	"SPARKWING_RUNNER_NODE_SELECTOR",
	"SPARKWING_RUNNER_TOLERATION",
	"SPARKWING_SECRETS_PROFILE",
	"SPARKWING_SQLITE_BUSY_TIMEOUT_MS",
	"SPARKWING_STOP_AT",
	"SPARKWING_STORE_WEDGE_BUDGET",
	"SPARKWING_TESTLEAK_HOST",
	"SPARKWING_TRIGGER_RUNNER",
	"SPARKWING_WINGD_VERSION",
}

var userNamedEnvReads = map[string]string{
	`internal/orchestrator/local_repo_resolver.go: "SPARKWING_REPO_" + envKeyForName(name)`: "one variable per repo, named after the repo",
	"pkg/backends/backends.go: s.TokenEnv":                                                  "the backend config says which variable holds its token",
	"pkg/storage/storeurl/spec.go: name":                                                    "a pipeline's url_source: names the variable holding its state URL",
	"sparkwing/inputs/inputs.go: name":                                                      "a pipeline declares which variables its inputs read",
	"sparkwing/source_resolver.go: key":                                                     "a secret source's configured prefix plus the secret's name",
}

func TestDocsNameEveryEnvironmentVariableTheCodeReads(t *testing.T) {
	names, dynamic, err := envVarsRead("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, site := range dynamic {
		if _, ok := userNamedEnvReads[site]; !ok {
			t.Errorf("%s computes an environment variable name; document a caller-selected name in userNamedEnvReads with its source", site)
		}
	}
	for site := range userNamedEnvReads {
		if !slices.Contains(dynamic, site) {
			t.Errorf("userNamedEnvReads acknowledges %q, which the walk no longer finds; drop it", site)
		}
	}
	if len(names) == 0 {
		t.Fatal("found no environment variable reads; source walk is incomplete")
	}

	documented := allDocsText(t)
	source := sourceDocsText(t)
	recorded := map[string]bool{}
	for _, name := range undocumentedEnvVars {
		recorded[name] = true
	}

	for _, name := range names {
		switch {
		case docsMentionEnvVar(documented, name) && recorded[name]:
			t.Errorf("%s is documented now; drop it from undocumentedEnvVars", name)
		case !docsMentionEnvVar(documented, name) && !recorded[name]:
			if docsMentionEnvVar(source, name) {
				t.Errorf("docs/ names %s but the embedded mirror this check reads does not; "+
					"run bash bin/sync-docs.sh and commit pkg/docs/", name)
				break
			}
			t.Errorf("no docs page names %s, which the code reads", name)
		}
	}

	read := map[string]bool{}
	for _, name := range names {
		read[name] = true
	}
	for _, name := range undocumentedEnvVars {
		if !read[name] {
			t.Errorf("undocumentedEnvVars lists unread variable %s; remove the entry", name)
		}
	}
}

func TestEnvVarWalkReadsNestedPackagesAndSkipsNestedModules(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("go.mod", "module fake\n\ngo 1.26\n")
	write("main.go", "package main\n\nimport \"os\"\n\nvar a = os.Getenv(\"SPARKWING_ROOT_VAR\")\n")
	write("internal/deep/deep.go", "package deep\n\nimport \"os\"\n\nvar b, _ = os.LookupEnv(\"SPARKWING_NESTED_VAR\")\n")
	write("internal/deep/other.go", "package deep\n\nimport \"os\"\n\nvar e = os.Getenv(\"KUBECONFIG\")\n")
	write("internal/deep/deep_test.go", "package deep\n\nimport \"os\"\n\nvar c = os.Getenv(\"SPARKWING_TEST_ONLY\")\n")
	write("testdata/fixture.go", "package fixture\n\nimport \"os\"\n\nvar f = os.Getenv(\"SPARKWING_FIXTURE\")\n")
	write("tools/go.mod", "module fake-tools\n\ngo 1.26\n")
	write("tools/tool.go", "package tools\n\nimport \"os\"\n\nvar d = os.Getenv(\"SPARKWING_OTHER_MODULE\")\n")

	runSnapshotGit(t, root, "init", "--quiet")
	runSnapshotGit(t, root, "add", "-A")
	names, dynamic, err := envVarsRead(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(dynamic) != 0 {
		t.Errorf("reported dynamic reads %v, want none", dynamic)
	}
	want := "SPARKWING_NESTED_VAR,SPARKWING_ROOT_VAR"
	if got := strings.Join(names, ","); got != want {
		t.Fatalf("envVarsRead found %q, want %q", got, want)
	}
}

func TestEnvVarWalkReportsAComputedRead(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module fake\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := "package main\n\nimport \"os\"\n\nfunc suffix() string { return \"X\" }\n\n" +
		"var v = os.Getenv(\"SPARKWING_REPO_\" + suffix())\n"
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	runSnapshotGit(t, root, "init", "--quiet")
	runSnapshotGit(t, root, "add", "-A")
	_, dynamic, err := envVarsRead(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(dynamic) != 1 || !strings.Contains(dynamic[0], "main.go") {
		t.Fatalf("dynamic reads %v, want one naming main.go", dynamic)
	}
	if !strings.Contains(dynamic[0], "suffix()") {
		t.Errorf("dynamic read reported as %q, want it to quote the expression", dynamic[0])
	}
}

func TestEnvVarWalkFollowsEnvHelpersToTheirCallSites(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module fake\n\ngo 1.26\n")
	write("env.go", "package main\n\nimport \"os\"\n\n"+
		"func envOr(name, fallback string) string {\n"+
		"\tif v := os.Getenv(name); v != \"\" {\n\t\treturn v\n\t}\n\treturn fallback\n}\n")
	write("main.go", "package main\n\nconst wedge = \"SPARKWING_WEDGE\"\n\n"+
		"var a = envOr(\"SPARKWING_VIA_HELPER\", \"\")\n"+
		"var b = envOr(wedge, \"\")\n")

	runSnapshotGit(t, root, "init", "--quiet")
	runSnapshotGit(t, root, "add", "-A")
	names, dynamic, err := envVarsRead(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(dynamic) != 0 {
		t.Errorf("reported dynamic reads %v, want helper reads resolved at their call sites", dynamic)
	}
	want := "SPARKWING_VIA_HELPER,SPARKWING_WEDGE"
	if got := strings.Join(names, ","); got != want {
		t.Errorf("envVarsRead found %q, want %q", got, want)
	}
}

func allDocsText(t *testing.T) string {
	t.Helper()
	var documentText strings.Builder
	for _, entry := range docs.List() {
		if matchesAny(entry.Slug, versionedPages) {
			continue
		}
		body, err := docs.ReadRaw(entry.Slug)
		if err != nil {
			t.Fatalf("read %s: %v", entry.Slug, err)
		}
		documentText.WriteString(body)
		documentText.WriteByte('\n')
	}
	return documentText.String()
}

func sourceDocsText(t *testing.T) string {
	t.Helper()
	root := filepath.FromSlash("../../docs")
	var documentText strings.Builder
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return walkErr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if matchesAny(strings.TrimSuffix(filepath.ToSlash(rel), ".md"), versionedPages) {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		documentText.Write(body)
		documentText.WriteByte('\n')
		return nil
	})
	if err != nil {
		t.Fatalf("read %s: %v", root, err)
	}
	return documentText.String()
}

func envVarsRead(root string) (names, dynamic []string, err error) {
	files, err := moduleFiles(root)
	if err != nil {
		return nil, nil, err
	}
	fileSet := token.NewFileSet()
	parsedFiles := make(map[string]*ast.File, len(files))
	for _, path := range files {
		file, parseErr := parser.ParseFile(fileSet, path, nil, 0)
		if parseErr != nil {
			return nil, nil, fmt.Errorf("parse %s: %w", path, parseErr)
		}
		parsedFiles[path] = file
	}
	constantValues := stringConstants(parsedFiles)

	helpers := envHelpers(parsedFiles)
	forwardedSites := forwardedReads(parsedFiles)

	namesRead := map[string]bool{}
	for path, file := range parsedFiles {
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			argumentIndex, ok := envReadArg(call.Fun, helpers)
			if !ok || argumentIndex >= len(call.Args) {
				return true
			}
			if forwardedSites[call.Pos()] {
				return true
			}
			v, ok := staticString(call.Args[argumentIndex], constantValues)
			if !ok {
				dynamic = append(dynamic, fmt.Sprintf("%s: %s", rel, exprText(fileSet, call.Args[argumentIndex])))
				return true
			}
			if strings.HasPrefix(v, envPrefix) {
				namesRead[v] = true
			}
			return true
		})
	}

	for name := range namesRead {
		names = append(names, name)
	}
	sort.Strings(names)
	sort.Strings(dynamic)
	return names, dynamic, nil
}

func moduleFiles(root string) ([]string, error) {
	cmd := exec.Command("git", "-C", root, "ls-files", "-z", "--", "*.go", "*go.mod")
	data, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list tracked module files: %w", err)
	}
	tracked := make(map[string]bool)
	for _, path := range strings.Split(string(data), "\x00") {
		tracked[path] = true
	}
	var out []string
files:
	for path := range tracked {
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			continue
		}
		for dir := filepath.Dir(path); dir != "."; dir = filepath.Dir(dir) {
			switch filepath.Base(dir) {
			case ".git", "testdata", "node_modules", "vendor":
				continue files
			}
			if tracked[filepath.ToSlash(filepath.Join(dir, "go.mod"))] {
				continue files
			}
		}
		out = append(out, filepath.Join(root, path))
	}
	sort.Strings(out)
	return out, nil
}

func stringConstants(files map[string]*ast.File) map[string]string {
	values := map[string]map[string]bool{}
	for _, file := range files {
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || (gen.Tok != token.CONST && gen.Tok != token.VAR) {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					v, err := strconv.Unquote(lit.Value)
					if err != nil {
						continue
					}
					if values[name.Name] == nil {
						values[name.Name] = map[string]bool{}
					}
					values[name.Name][v] = true
				}
			}
		}
	}
	out := map[string]string{}
	for name, vs := range values {
		if len(vs) != 1 {
			continue
		}
		for v := range vs {
			out[name] = v
		}
	}
	return out
}

func staticString(arg ast.Expr, constantValues map[string]string) (string, bool) {
	switch e := arg.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		v, err := strconv.Unquote(e.Value)
		return v, err == nil
	case *ast.Ident:
		v, ok := constantValues[e.Name]
		return v, ok
	case *ast.SelectorExpr:
		v, ok := constantValues[e.Sel.Name]
		return v, ok
	}
	return "", false
}

func isEnvRead(fun ast.Expr) bool {
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "os" {
		return false
	}
	return sel.Sel.Name == "Getenv" || sel.Sel.Name == "LookupEnv"
}

func envReadArg(fun ast.Expr, helpers map[string]int) (int, bool) {
	if isEnvRead(fun) {
		return 0, true
	}
	switch e := fun.(type) {
	case *ast.Ident:
		i, ok := helpers[e.Name]
		return i, ok
	case *ast.SelectorExpr:
		i, ok := helpers[e.Sel.Name]
		return i, ok
	}
	return 0, false
}

func envHelpers(files map[string]*ast.File) map[string]int {
	out := map[string]int{}
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Type.Params == nil {
				continue
			}
			params := map[string]int{}
			i := 0
			for _, field := range fn.Type.Params.List {
				for _, name := range field.Names {
					params[name.Name] = i
					i++
				}
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || !isEnvRead(call.Fun) || len(call.Args) == 0 {
					return true
				}
				ident, ok := call.Args[0].(*ast.Ident)
				if !ok {
					return true
				}
				if idx, ok := params[ident.Name]; ok {
					out[fn.Name.Name] = idx
				}
				return true
			})
		}
	}
	return out
}

func forwardedReads(files map[string]*ast.File) map[token.Pos]bool {
	out := map[token.Pos]bool{}
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			var functionType *ast.FuncType
			var body *ast.BlockStmt
			switch fn := n.(type) {
			case *ast.FuncDecl:
				functionType, body = fn.Type, fn.Body
			case *ast.FuncLit:
				functionType, body = fn.Type, fn.Body
			default:
				return true
			}
			if body == nil {
				return true
			}
			params := paramNames(functionType)
			ast.Inspect(body, func(inner ast.Node) bool {
				call, ok := inner.(*ast.CallExpr)
				if !ok || !isEnvRead(call.Fun) || len(call.Args) == 0 {
					return true
				}
				if ident, ok := call.Args[0].(*ast.Ident); ok && params[ident.Name] {
					out[call.Pos()] = true
				}
				return true
			})
			return true
		})
	}
	return out
}

func paramNames(functionType *ast.FuncType) map[string]bool {
	out := map[string]bool{}
	if functionType == nil || functionType.Params == nil {
		return out
	}
	for _, field := range functionType.Params.List {
		for _, name := range field.Names {
			out[name.Name] = true
		}
	}
	return out
}

func exprText(fileSet *token.FileSet, e ast.Expr) string {
	var b strings.Builder
	if err := printer.Fprint(&b, fileSet, e); err != nil {
		return "<unprintable>"
	}
	return b.String()
}

func TestEnvVarWalkIgnoresUntrackedScratch(t *testing.T) {
	root := t.TempDir()
	runSnapshotGit(t, root, "init", "--quiet")
	tracked := filepath.Join(root, "main.go")
	body := "package main\nimport \"os\"\nvar value = os.Getenv(\"SPARKWING_TRACKED\")\n"
	if err := os.WriteFile(tracked, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	runSnapshotGit(t, root, "add", "main.go")
	for _, rel := range []string{"scratch.go", ".claude-scratch/backup.go"} {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(strings.ReplaceAll(body, "SPARKWING_TRACKED", "SPARKWING_SCRATCH")), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	names, _, err := envVarsRead(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(names, ","); got != "SPARKWING_TRACKED" {
		t.Fatalf("reads = %q, want only the tracked variable", got)
	}
	runSnapshotGit(t, root, "add", "scratch.go")
	names, _, err = envVarsRead(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(names, ","); got != "SPARKWING_SCRATCH,SPARKWING_TRACKED" {
		t.Fatalf("reads after staging scratch = %q", got)
	}
}
