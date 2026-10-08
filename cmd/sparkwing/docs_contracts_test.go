package main

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

var userNamedEnvReads = map[string]string{
	`internal/orchestrator/local_repo_resolver.go: "SPARKWING_REPO_" + envKeyForName(name)`: "one variable per repo, named after the repo",
	"pkg/backends/backends.go: s.TokenEnv":                                                  "the backend config says which variable holds its token",
	"pkg/storage/storeurl/spec.go: name":                                                    "a pipeline's url_source: names the variable holding its state URL",
	"sparkwing/inputs/inputs.go: name":                                                      "a pipeline declares which variables its inputs read",
	"internal/secretsource/secretsource.go: key":                                            "a secret source's configured prefix plus the secret's name",
}

// safety: a computed read hides the names it produces, so each one is listed
// here and the page check covers them like any other read.
var constructedEnvReads = map[string][]string{
	"internal/objectguard/config.go: classEnv(c, WindowMinute)": {
		"SPARKWING_OBJECT_STORE_PUT_PER_MINUTE", "SPARKWING_OBJECT_STORE_GET_PER_MINUTE",
		"SPARKWING_OBJECT_STORE_LIST_PER_MINUTE", "SPARKWING_OBJECT_STORE_DELETE_PER_MINUTE",
	},
	"internal/objectguard/config.go: classEnv(c, WindowDay)": {
		"SPARKWING_OBJECT_STORE_PUT_PER_DAY", "SPARKWING_OBJECT_STORE_GET_PER_DAY",
		"SPARKWING_OBJECT_STORE_LIST_PER_DAY", "SPARKWING_OBJECT_STORE_DELETE_PER_DAY",
	},
	// safety: only sparkwing-controller hands egress.Bind an environment.
	"internal/egress/flags.go: name": {
		"SPARKWING_CONTROLLER_EGRESS_DAILY_ALARM_BYTES", "SPARKWING_CONTROLLER_EGRESS_MAX_DOWNLOADS",
		"SPARKWING_CONTROLLER_EGRESS_MAX_LOG_STREAMS",
	},
	"internal/executorinfo/platform.go: key": {"WSL_INTEROP", "WSL_DISTRO_NAME"},
}

var envPageRow = regexp.MustCompile("(?m)^\\| `([A-Z][A-Z0-9_]*)` \\|")

func TestDocsNameEveryEnvironmentVariableTheCodeReads(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.3s of real work; the fast class runs under -short")
	}
	names, dynamic, err := envVarsRead("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, site := range dynamic {
		if constructed, ok := constructedEnvReads[site]; ok {
			names = append(names, constructed...)
			continue
		}
		if _, ok := userNamedEnvReads[site]; !ok {
			t.Errorf("%s computes an environment variable name; list what it produces in constructedEnvReads, "+
				"or document a caller-selected name in userNamedEnvReads with its source", site)
		}
	}
	for site := range userNamedEnvReads {
		if !slices.Contains(dynamic, site) {
			t.Errorf("userNamedEnvReads acknowledges %q, which the walk no longer finds; drop it", site)
		}
	}
	for site := range constructedEnvReads {
		if !slices.Contains(dynamic, site) {
			t.Errorf("constructedEnvReads lists %q, which the walk no longer finds; drop it", site)
		}
	}
	slices.Sort(names)
	names = slices.Compact(names)
	if len(names) == 0 {
		t.Fatal("found no environment variable reads; source walk is incomplete")
	}

	documented := allDocsText(t)
	source := sourceDocsText(t)
	for _, name := range names {
		if docsMentionEnvVar(documented, name) {
			continue
		}
		if docsMentionEnvVar(source, name) {
			t.Errorf("docs/ names %s but the embedded mirror this check reads does not; "+
				"run bash bin/sync-docs.sh and commit pkg/docs/", name)
			continue
		}
		t.Errorf("no docs page names %s, which the code reads; add it to docs/environment-variables.md", name)
	}

	reference, err := docs.ReadRaw("environment-variables")
	if err != nil {
		t.Fatal(err)
	}
	read := map[string]bool{}
	for _, name := range names {
		read[name] = true
		if !docsMentionEnvVar(reference, name) {
			t.Errorf("docs/environment-variables.md does not list %s, which the code reads", name)
		}
	}
	listed := envVarTokens.FindAllString(reference, -1)
	for _, row := range envPageRow.FindAllStringSubmatch(reference, -1) {
		listed = append(listed, row[1])
	}
	for _, name := range listed {
		if !read[name] {
			t.Errorf("docs/environment-variables.md lists %s, which nothing reads; remove its row", name)
		}
	}
}

var envVarTokens = regexp.MustCompile(`\b` + envPrefix + `[A-Z0-9_]+\b`)

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
	want := "KUBECONFIG,SPARKWING_NESTED_VAR,SPARKWING_ROOT_VAR"
	if got := strings.Join(names, ","); got != want {
		t.Fatalf("envVarsRead found %q, want %q", got, want)
	}
}

func TestEnvVarWalkFollowsAnInjectedGetenv(t *testing.T) {
	root := t.TempDir()
	writeFixtureModule(t, root, map[string]string{
		"go.mod": "module fake\n\ngo 1.26\n",
		"cfg.go": "package main\n\nconst prefix = \"SPARKWING_FOLDED_\"\nconst folded = prefix + \"NAME\"\n\n" +
			"func load(getenv func(string) string) string { return getenv(\"SPARKWING_INJECTED\") + inner(getenv, folded) }\n\n" +
			"func inner(getenv func(string) string, name string) string { return getenv(name) }\n\n" +
			"func unrelated(f func(string) string) string { return f(\"SPARKWING_NOT_ENV\") }\n",
		"lookup.go": "package main\n\nimport \"os\"\n\nvar lookupEnv = os.LookupEnv\n\n" +
			"func lookup() bool { _, ok := lookupEnv(\"SPARKWING_ALIASED\"); return ok }\n",
		"main.go": "package main\n\nimport (\n\t\"os\"\n\t\"strings\"\n)\n\n" +
			"var a = load(os.Getenv)\n\nvar b = unrelated(strings.ToUpper)\n\nfunc main() { _ = load(os.Getenv) }\n",
	})
	names, dynamic, err := envVarsRead(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(dynamic) != 0 {
		t.Errorf("dynamic reads %v, want every injected read resolved", dynamic)
	}
	want := "SPARKWING_ALIASED,SPARKWING_FOLDED_NAME,SPARKWING_INJECTED"
	if got := strings.Join(names, ","); got != want {
		t.Errorf("envVarsRead found %q, want %q", got, want)
	}
}

func TestEnvVarWalkReadsAnEnvironmentScanButNotAFilter(t *testing.T) {
	root := t.TempDir()
	writeFixtureModule(t, root, map[string]string{
		"go.mod": "module fake\n\ngo 1.26\n",
		"main.go": "package main\n\nimport (\n\t\"os\"\n\t\"strings\"\n)\n\nconst allowKey = \"SPARKWING_SCANNED\"\n\n" +
			"func allow() string {\n\tfor _, entry := range os.Environ() {\n\t\tkey, value, ok := strings.Cut(entry, \"=\")\n" +
			"\t\tif !ok || key != allowKey {\n\t\t\tcontinue\n\t\t}\n\t\treturn value\n\t}\n\treturn \"\"\n}\n\n" +
			"func strip(env []string) []string {\n\tvar out []string\n\tfor _, entry := range env {\n" +
			"\t\tname, _, _ := strings.Cut(entry, \"=\")\n\t\tswitch {\n\t\tcase name == \"SPARKWING_DROPPED\":\n" +
			"\t\tdefault:\n\t\t\tout = append(out, entry)\n\t\t}\n\t}\n\treturn out\n}\n",
	})
	names, _, err := envVarsRead(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(names, ","); got != "SPARKWING_SCANNED" {
		t.Errorf("envVarsRead found %q, want the scanned key alone", got)
	}
}

func TestEnvVarWalkSkipsTrackedFilesDeletedFromWorktree(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module fake\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("package main\n\nimport \"os\"\n\nvar a = os.Getenv(\"SPARKWING_REMOVED\")\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runSnapshotGit(t, root, "init", "--quiet")
	runSnapshotGit(t, root, "add", "-A")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	names, dynamic, err := envVarsRead(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 0 || len(dynamic) != 0 {
		t.Fatalf("deleted file contributed env reads: names=%v dynamic=%v", names, dynamic)
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

func TestEnvVarWalkResolvesSameNamedConstantsPerPackage(t *testing.T) {
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
	write("internal/fleet/fleet.go", "package fleet\n\nimport \"os\"\n\n"+
		"const PathEnv = \"SPARKWING_ALPHA_PATH\"\n\nvar a = os.Getenv(PathEnv)\n")
	write("internal/repos/repos.go", "package repos\n\nimport \"os\"\n\n"+
		"const PathEnv = \"SPARKWING_BETA_PATH\"\n\nvar b = os.Getenv(PathEnv)\n")
	write("main.go", "package main\n\nimport (\n\t\"os\"\n\n\tf \"fake/internal/fleet\"\n"+
		"\t\"fake/internal/repos\"\n\t\"example.com/outside\"\n)\n\n"+
		"var c = os.Getenv(f.PathEnv)\n"+
		"var d = os.Getenv(repos.PathEnv)\n"+
		"var e = os.Getenv(outside.PathEnv)\n")

	runSnapshotGit(t, root, "init", "--quiet")
	runSnapshotGit(t, root, "add", "-A")
	names, dynamic, err := envVarsRead(root)
	if err != nil {
		t.Fatal(err)
	}
	want := "SPARKWING_ALPHA_PATH,SPARKWING_BETA_PATH"
	if got := strings.Join(names, ","); got != want {
		t.Errorf("envVarsRead found %q, want %q", got, want)
	}
	if len(dynamic) != 1 || !strings.Contains(dynamic[0], "outside.PathEnv") {
		t.Errorf("dynamic reads %v, want only the constant outside this module", dynamic)
	}
}

func writeFixtureModule(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runSnapshotGit(t, root, "init", "--quiet")
	runSnapshotGit(t, root, "add", "-A")
}

// Two directories whose packages share a name used to leave the qualifier
// pointing at whichever import came last, so a read of one package's constant
// recorded the other package's value.
func TestEnvVarWalkRefusesAQualifierTwoImportsAnswerTo(t *testing.T) {
	root := t.TempDir()
	writeFixtureModule(t, root, map[string]string{
		"go.mod":          "module fake\n\ngo 1.26\n",
		"internal/a/x.go": "package beta\n\nconst K = \"SPARKWING_FROM_A\"\n",
		"internal/b/y.go": "package beta\n\nconst K = \"SPARKWING_FROM_B\"\n",
		"main.go": "package main\n\nimport (\n\t\"os\"\n\n\t\"fake/internal/b\"\n" +
			"\t\"fake/internal/a\"\n)\n\nvar a = os.Getenv(beta.K)\n",
	})
	names, dynamic, err := envVarsRead(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 0 {
		t.Errorf("envVarsRead resolved %v through a qualifier two imports answer to", names)
	}
	if len(dynamic) != 1 || !strings.Contains(dynamic[0], "beta.K") {
		t.Errorf("dynamic reads %v, want the ambiguous qualifier alone", dynamic)
	}
}

// A directory whose files disagree on the package name used to take whichever
// name the file walk reached last, which is map order.
func TestEnvVarWalkRefusesADisputedPackageName(t *testing.T) {
	root := t.TempDir()
	writeFixtureModule(t, root, map[string]string{
		"go.mod":                "module fake\n\ngo 1.26\n",
		"internal/dup/alpha.go": "package alpha\n\nconst K = \"SPARKWING_ALPHA\"\n",
		"internal/dup/beta.go":  "package beta\n",
		"main.go": "package main\n\nimport (\n\t\"os\"\n\n\t\"fake/internal/dup\"\n)\n\n" +
			"var a = os.Getenv(alpha.K)\n",
	})
	// safety: the old shape answered out of map order, so one run proves
	// nothing about the next.
	for range 10 {
		names, dynamic, err := envVarsRead(root)
		if err != nil {
			t.Fatal(err)
		}
		if len(names) != 0 {
			t.Fatalf("envVarsRead resolved %v through a disputed directory", names)
		}
		if len(dynamic) != 1 || !strings.Contains(dynamic[0], "alpha.K") {
			t.Fatalf("dynamic reads %v, want the unresolvable qualifier alone", dynamic)
		}
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
	importedDirs := importQualifiers(root, parsedFiles)

	readerCalls := envReaderCalls(parsedFiles)
	helpers := envHelpers(parsedFiles, readerCalls)
	forwardedSites := forwardedReads(parsedFiles, readerCalls)

	namesRead := map[string]bool{}
	for path, file := range parsedFiles {
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		scope := constantScope{
			dir:       filepath.Dir(path),
			qualifier: importedDirs[path],
			values:    constantValues,
		}
		for _, name := range envScanReads(file, scope) {
			namesRead[name] = true
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			argumentIndex, ok := envReadArg(call.Fun, helpers)
			if !ok && readerCalls[call.Pos()] {
				argumentIndex, ok = 0, true
			}
			if !ok || argumentIndex >= len(call.Args) {
				return true
			}
			if forwardedSites[call.Pos()] {
				return true
			}
			v, ok := staticString(call.Args[argumentIndex], scope)
			if !ok {
				dynamic = append(dynamic, fmt.Sprintf("%s: %s", rel, exprText(fileSet, call.Args[argumentIndex])))
				return true
			}
			if envNameShape.MatchString(v) {
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
		fullPath := filepath.Join(root, path)
		if _, err := os.Stat(fullPath); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		out = append(out, fullPath)
	}
	sort.Strings(out)
	return out, nil
}

type constantKey struct {
	dir  string
	name string
}

type constantScope struct {
	dir       string
	qualifier map[string]string
	values    map[constantKey]string
}

func stringConstants(files map[string]*ast.File) map[constantKey]string {
	type pending struct {
		key  constantKey
		expr ast.Expr
	}
	var specs []pending
	for path, file := range files {
		dir := filepath.Dir(path)
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
					if i < len(vs.Values) {
						specs = append(specs, pending{constantKey{dir: dir, name: name.Name}, vs.Values[i]})
					}
				}
			}
		}
	}
	// safety: a name declared twice in one directory with different values
	// resolves to neither, so a read of it stays dynamic rather than guessed.
	out := map[constantKey]string{}
	disputed := map[constantKey]bool{}
	for changed := true; changed; {
		changed = false
		for _, p := range specs {
			if disputed[p.key] {
				continue
			}
			v, ok := foldString(p.expr, p.key.dir, out)
			if !ok {
				continue
			}
			if seen, done := out[p.key]; done {
				if seen != v {
					delete(out, p.key)
					disputed[p.key] = true
					changed = true
				}
				continue
			}
			out[p.key] = v
			changed = true
		}
	}
	return out
}

func foldString(e ast.Expr, dir string, known map[constantKey]string) (string, bool) {
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return "", false
		}
		v, err := strconv.Unquote(x.Value)
		return v, err == nil
	case *ast.Ident:
		v, ok := known[constantKey{dir: dir, name: x.Name}]
		return v, ok
	case *ast.ParenExpr:
		return foldString(x.X, dir, known)
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return "", false
		}
		left, ok := foldString(x.X, dir, known)
		if !ok {
			return "", false
		}
		right, ok := foldString(x.Y, dir, known)
		return left + right, ok
	}
	return "", false
}

func importQualifiers(root string, files map[string]*ast.File) map[string]map[string]string {
	module := moduleName(root)
	// safety: map order decides nothing here. A directory whose files disagree
	// on the package name is dropped, so an import of it resolves to no
	// qualifier and its reads stay dynamic, rather than resolving to whichever
	// file the range reached last.
	packageNames := map[string]string{}
	disputed := map[string]bool{}
	for path, file := range files {
		dir := filepath.Dir(path)
		if seen, ok := packageNames[dir]; ok && seen != file.Name.Name {
			disputed[dir] = true
			continue
		}
		packageNames[dir] = file.Name.Name
	}
	for dir := range disputed {
		delete(packageNames, dir)
	}
	out := make(map[string]map[string]string, len(files))
	for path, file := range files {
		if module == "" {
			continue
		}
		qualifiers := map[string]string{}
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				continue
			}
			rel, inModule := strings.CutPrefix(importPath, module)
			if !inModule || (rel != "" && !strings.HasPrefix(rel, "/")) {
				continue
			}
			dir := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(rel, "/")))
			name := packageNames[dir]
			if spec.Name != nil {
				name = spec.Name.Name
			}
			if name == "" || name == "_" || name == "." {
				continue
			}
			// safety: two imports answering to one qualifier name would leave
			// the reads of one of them reading the other's constants, so
			// neither resolves.
			if seen, ok := qualifiers[name]; ok && seen != dir {
				qualifiers[name] = ""
				continue
			}
			if _, poisoned := qualifiers[name]; !poisoned {
				qualifiers[name] = dir
			}
		}
		out[path] = qualifiers
	}
	return out
}

func moduleName(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

func staticString(arg ast.Expr, scope constantScope) (string, bool) {
	switch e := arg.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		v, err := strconv.Unquote(e.Value)
		return v, err == nil
	case *ast.Ident:
		v, ok := scope.values[constantKey{dir: scope.dir, name: e.Name}]
		return v, ok
	case *ast.SelectorExpr:
		pkg, ok := e.X.(*ast.Ident)
		if !ok {
			return "", false
		}
		// safety: this reads the syntax alone, so it cannot tell an import
		// qualifier from a local variable, field or parameter of the same name;
		// a local shadowing an imported package resolves to that package's
		// constant. No site in this module shadows one.
		dir, ok := scope.qualifier[pkg.Name]
		if !ok || dir == "" {
			return "", false
		}
		v, ok := scope.values[constantKey{dir: dir, name: e.Sel.Name}]
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

func envHelpers(files map[string]*ast.File, readerCalls map[token.Pos]bool) map[string]int {
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
				if !ok || !(isEnvRead(call.Fun) || readerCalls[call.Pos()]) || len(call.Args) == 0 {
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

func forwardedReads(files map[string]*ast.File, readerCalls map[token.Pos]bool) map[token.Pos]bool {
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
				if !ok || !(isEnvRead(call.Fun) || readerCalls[call.Pos()]) || len(call.Args) == 0 {
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

var envNameShape = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// safety: a read through a function value hides from a walk of os.Getenv calls,
// so a package variable bound to os.Getenv or os.LookupEnv, and a parameter a
// caller hands one to, count too, followed through every function passing it on.
func envReaderCalls(files map[string]*ast.File) map[token.Pos]bool {
	decls := map[string][]*ast.FuncDecl{}
	aliases := map[string]bool{}
	for _, file := range files {
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Body != nil {
					decls[d.Name.Name] = append(decls[d.Name.Name], d)
				}
			case *ast.GenDecl:
				if d.Tok != token.VAR {
					continue
				}
				for _, spec := range d.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, name := range vs.Names {
						if i < len(vs.Values) && isEnvFunc(vs.Values[i]) {
							aliases[name.Name] = true
						}
					}
				}
			}
		}
	}
	marked := map[string]map[int]bool{}
	readers := func(fn *ast.FuncDecl) map[string]bool {
		out := maps.Clone(aliases)
		for i, name := range orderedParams(fn.Type) {
			if marked[fn.Name.Name][i] {
				out[name] = true
			}
		}
		return out
	}
	for changed := true; changed; {
		changed = false
		for _, fns := range decls {
			for _, fn := range fns {
				scope := readers(fn)
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					callee := calleeName(call.Fun)
					for i, arg := range call.Args {
						if !isEnvFunc(arg) && !isScopedReader(arg, scope) {
							continue
						}
						for _, target := range decls[callee] {
							if !readerParam(target.Type, i) || marked[callee][i] {
								continue
							}
							if marked[callee] == nil {
								marked[callee] = map[int]bool{}
							}
							marked[callee][i] = true
							changed = true
						}
					}
					return true
				})
			}
		}
	}
	sites := map[token.Pos]bool{}
	for _, fns := range decls {
		for _, fn := range fns {
			scope := readers(fn)
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok && len(call.Args) > 0 && isScopedReader(call.Fun, scope) {
					sites[call.Pos()] = true
				}
				return true
			})
		}
	}
	return sites
}

func isEnvFunc(e ast.Expr) bool {
	return isSelector(e, "os", "Getenv") || isSelector(e, "os", "LookupEnv")
}

func isScopedReader(e ast.Expr, scope map[string]bool) bool {
	ident, ok := e.(*ast.Ident)
	return ok && scope[ident.Name]
}

func calleeName(fun ast.Expr) string {
	switch e := fun.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return e.Sel.Name
	}
	return ""
}

func orderedParams(functionType *ast.FuncType) []string {
	var out []string
	if functionType == nil || functionType.Params == nil {
		return out
	}
	for _, field := range functionType.Params.List {
		for _, name := range field.Names {
			out = append(out, name.Name)
		}
	}
	return out
}

func readerParam(functionType *ast.FuncType, i int) bool {
	if functionType.Params == nil {
		return false
	}
	at := 0
	for _, field := range functionType.Params.List {
		count := max(len(field.Names), 1)
		if i < at+count {
			ft, ok := field.Type.(*ast.FuncType)
			if !ok || ft.Params == nil || len(ft.Params.List) != 1 || !isIdent(ft.Params.List[0].Type, "string") {
				return false
			}
			if ft.Results == nil {
				return false
			}
			var results []ast.Expr
			for _, r := range ft.Results.List {
				for range max(len(r.Names), 1) {
					results = append(results, r.Type)
				}
			}
			switch len(results) {
			case 1:
				return isIdent(results[0], "string")
			case 2:
				return isIdent(results[0], "string") && isIdent(results[1], "bool")
			}
			return false
		}
		at += count
	}
	return false
}

func isIdent(e ast.Expr, name string) bool {
	ident, ok := e.(*ast.Ident)
	return ok && ident.Name == name
}

// hack: an environment scan is spotted by shape: strings.Cut(entry, "=") over
// os.Environ() or a slice named like an environment, its key compared in an if.
func envScanReads(file *ast.File, scope constantScope) []string {
	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		loop, ok := n.(*ast.RangeStmt)
		if !ok || !rangesOverEnvironment(loop.X) {
			return true
		}
		entry, ok := loop.Value.(*ast.Ident)
		if !ok {
			return true
		}
		keys := map[string]bool{}
		ast.Inspect(loop.Body, func(inner ast.Node) bool {
			switch node := inner.(type) {
			case *ast.AssignStmt:
				if len(node.Rhs) != 1 || len(node.Lhs) == 0 {
					return true
				}
				call, ok := node.Rhs[0].(*ast.CallExpr)
				if !ok || len(call.Args) != 2 || !isSelector(call.Fun, "strings", "Cut") || !isIdent(call.Args[0], entry.Name) {
					return true
				}
				if sep, ok := staticString(call.Args[1], scope); !ok || sep != "=" {
					return true
				}
				if key, ok := node.Lhs[0].(*ast.Ident); ok {
					keys[key.Name] = true
				}
			case *ast.IfStmt:
				// safety: only an if picks one entry to read; a switch over the
				// key is how a filter drops names, which reads none of them.
				ast.Inspect(node.Cond, func(c ast.Node) bool {
					cmp, ok := c.(*ast.BinaryExpr)
					if !ok || (cmp.Op != token.EQL && cmp.Op != token.NEQ) {
						return true
					}
					for _, pair := range [][2]ast.Expr{{cmp.X, cmp.Y}, {cmp.Y, cmp.X}} {
						if key, ok := pair[0].(*ast.Ident); ok && keys[key.Name] {
							if v, ok := staticString(pair[1], scope); ok && envNameShape.MatchString(v) {
								out = append(out, v)
							}
						}
					}
					return true
				})
			}
			return true
		})
		return true
	})
	return out
}

func rangesOverEnvironment(e ast.Expr) bool {
	if call, ok := e.(*ast.CallExpr); ok {
		return isSelector(call.Fun, "os", "Environ")
	}
	ident, ok := e.(*ast.Ident)
	return ok && strings.Contains(strings.ToLower(ident.Name), "env")
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
