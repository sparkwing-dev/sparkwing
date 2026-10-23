package acceptance_test

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var updateCurrentContracts = flag.Bool("update-current-contracts", false, "regenerate the settings symbol inventory")

func contractSymbols(t *testing.T, directory string) map[string]string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(directory, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	symbols := map[string]string{}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range file.Decls {
			switch d := declaration.(type) {
			case *ast.FuncDecl:
				if !d.Name.IsExported() {
					continue
				}
				name := d.Name.Name
				if d.Recv != nil {
					receiver := d.Recv.List[0].Type
					if pointer, ok := receiver.(*ast.StarExpr); ok {
						receiver = pointer.X
					}
					switch generic := receiver.(type) {
					case *ast.IndexExpr:
						receiver = generic.X
					case *ast.IndexListExpr:
						receiver = generic.X
					}
					id, ok := receiver.(*ast.Ident)
					if !ok {
						continue
					}
					name = id.Name + "." + name
				}
				symbols[name] = filepath.Base(path)
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if !s.Name.IsExported() {
							continue
						}
						symbols[s.Name.Name] = filepath.Base(path)
						var fields *ast.FieldList
						switch kind := s.Type.(type) {
						case *ast.StructType:
							fields = kind.Fields
						case *ast.InterfaceType:
							fields = kind.Methods
						}
						if fields != nil {
							for _, f := range fields.List {
								for _, name := range f.Names {
									if name.IsExported() {
										symbols[s.Name.Name+"."+name.Name] = filepath.Base(path)
									}
								}
							}
						}
					case *ast.ValueSpec:
						for _, name := range s.Names {
							if name.IsExported() {
								symbols[name.Name] = filepath.Base(path)
							}
						}
					}
				}
			}
		}
	}
	return symbols
}

func TestCurrentContractSymbolInventories(t *testing.T) {
	const root = "../../"
	packages := map[string]map[string]string{}
	for _, name := range []string{"settings", "nodehost", "store", "capability", "controllerwire", "runspec", "runstate", "engineid", "localhostwire", "routepolicy", "blob", "lease", "source", "engine", "nodeproto", "apierr", "controller", "protofd"} {
		packages[name] = contractSymbols(t, root+"internal/"+name)
	}
	packages["client"] = contractSymbols(t, root+"internal/controller/client")
	path := root + "design/rewrite/contracts/c4-surface-config.md"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	before, rest, ok := strings.Cut(string(data), "### internal/settings\n")
	if !ok {
		t.Fatal("missing settings inventory")
	}
	_, after, ok := strings.Cut(rest, "The restricted declaration types live")
	if !ok {
		t.Fatal("missing settings inventory end")
	}
	names := []string{}
	for name := range packages["settings"] {
		if !strings.Contains(name, ".") {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	var table strings.Builder
	table.WriteString("\nGenerated from exported settings declarations by `TestCurrentContractSymbolInventories`;\nregenerate with `go test ./tests/acceptance -run '^TestCurrentContractSymbolInventories$' -update-current-contracts`.\n\n| Export | Source |\n| --- | --- |\n")
	for _, name := range names {
		fmt.Fprintf(&table, "| `%s` | [source](../../../internal/settings/%s) |\n", name, packages["settings"][name])
	}
	expected := before + "### internal/settings\n" + table.String() + "\nThe restricted declaration types live" + after
	if string(data) != expected {
		if *updateCurrentContracts {
			if err := os.WriteFile(path, []byte(expected), 0o644); err != nil {
				t.Fatal(err)
			}
		} else {
			t.Error("settings inventory differs from source; regenerate with -update-current-contracts")
		}
	}
	for _, document := range []string{"c1-data.md", "c4-surface-config.md"} {
		data, err := os.ReadFile(root + "design/rewrite/contracts/" + document)
		if err != nil {
			t.Fatal(err)
		}
		for _, section := range strings.Split(string(data), "\n### ")[1:] {
			heading, body, _ := strings.Cut(section, "\n")
			if !strings.HasPrefix(heading, "internal/") && !strings.HasPrefix(heading, "pkg/") {
				continue
			}
			owner := strings.TrimSuffix(heading, ".go")
			if strings.HasSuffix(heading, ".go") {
				if _, err := os.Stat(root + heading); err != nil {
					t.Fatalf("inventory source %s: %v", heading, err)
				}
				owner = filepath.Dir(heading)
			}
			if packages[owner] == nil {
				packages[owner] = contractSymbols(t, root+owner)
			}
			body, _, _ = strings.Cut(body, "\n## ")
			checkContractInventory(t, body, owner, packages)
		}
	}
	for _, inventory := range []struct{ document, start, end, owner string }{
		{"c3-protocol-sdk.md", "## Host and store export inventory\n", "## Host invariants", ""},
		{"c3-engine-version.md", "## Export inventory\n", "\n## ", "engine"},
		{"c3-protocol-notes.md", "## Exported inventory\n", "## Wire invariants", "nodeproto"},
	} {
		data, err := os.ReadFile(root + "design/rewrite/contracts/" + inventory.document)
		if err != nil {
			t.Fatal(err)
		}
		_, body, ok := strings.Cut(string(data), inventory.start)
		if !ok {
			t.Fatalf("missing inventory in %s", inventory.document)
		}
		body, _, _ = strings.Cut(body, inventory.end)
		checkContractInventory(t, body, inventory.owner, packages)
	}
	data, err = os.ReadFile(root + "design/rewrite/contracts/c2-credentials.md")
	if err != nil {
		t.Fatal(err)
	}
	_, inventory, ok := strings.Cut(string(data), "## Exported API\n")
	if !ok {
		t.Fatal("missing capability inventory")
	}
	inventory, _, _ = strings.Cut(inventory, "\nThe capability tables stay private")
	checkContractInventory(t, inventory, "capability", packages)
	data, err = os.ReadFile(root + "design/rewrite/contracts/c3-controller-wire.md")
	if err != nil {
		t.Fatal(err)
	}
	_, inventory, ok = strings.Cut(string(data), "| Export | Contract |\n")
	if !ok {
		t.Fatal("missing controller wire inventory")
	}
	inventory, _, _ = strings.Cut(inventory, "\nThe expansion adapter")
	checkContractInventory(t, inventory, "controllerwire", packages)
	data, err = os.ReadFile(root + "design/rewrite/contracts/c3-go-sdk-notes.md")
	if err != nil {
		t.Fatal(err)
	}
	_, sdk, ok := strings.Cut(string(data), "### New\n")
	if !ok {
		t.Fatal("missing SDK inventory")
	}
	sdk, _, ok = strings.Cut(sdk, "### Delete\n")
	if !ok {
		t.Fatal("missing SDK inventory end")
	}
	quoted := regexp.MustCompile("`([^`]+)`")
	for _, line := range strings.Split(sdk, "\n") {
		cells := strings.Split(line, "|")
		if len(cells) < 4 {
			continue
		}
		paths := quoted.FindStringSubmatch(cells[2])
		if len(paths) == 0 {
			continue
		}
		directory := filepath.Dir(paths[1])
		owner := filepath.Base(directory)
		if packages[owner] == nil {
			packages[owner] = contractSymbols(t, root+directory)
		}
		checkContractInventory(t, line, owner, packages)
	}
	_, fake, ok := strings.Cut(string(data), "## Fake-host method inventory\n")
	if !ok {
		t.Fatal("missing fake-host inventory")
	}
	fake, _, ok = strings.Cut(fake, "## Retained constants")
	if !ok {
		t.Fatal("missing fake-host inventory end")
	}
	packages["sparkwingtest"] = contractSymbols(t, root+"sparkwing/sparkwingtest")
	checkContractInventory(t, fake, "sparkwingtest", packages)

}

func checkContractInventory(t *testing.T, inventory, defaultOwner string, packages map[string]map[string]string) {
	t.Helper()
	quoted := regexp.MustCompile("`([^`]+)`")
	for _, line := range strings.Split(inventory, "\n") {
		cells := strings.Split(line, "|")
		if len(cells) < 3 {
			continue
		}
		owner := defaultOwner
		for _, match := range quoted.FindAllStringSubmatch(cells[1], -1) {
			symbol := strings.NewReplacer("(*", "", ")", "").Replace(match[1])
			if prefix, name, found := strings.Cut(symbol, "."); found && packages[prefix] != nil {
				owner, symbol = prefix, name
			}
			if owner == "" || packages[owner][symbol] == "" {
				t.Errorf("inventory names missing symbol %s.%s", owner, symbol)
			}
		}
	}
}

func TestContractSourceSymbolLinks(t *testing.T) {
	paths, err := filepath.Glob("../../design/rewrite/contracts/*.md")
	if err != nil || len(paths) == 0 {
		t.Fatalf("contracts: %v", err)
	}
	links := regexp.MustCompile("\\[`([A-Za-z][A-Za-z0-9_.]+)`\\]\\(([^)]+\\.go)\\)")
	packages := map[string]map[string]string{}
	checked := 0
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, ref := range links.FindAllStringSubmatch(string(body), -1) {
			checked++
			source := filepath.Join(filepath.Dir(path), ref[2])
			directory := filepath.Dir(source)
			symbols, ok := packages[directory]
			if !ok {
				symbols = contractSymbols(t, directory)
				packages[directory] = symbols
			}
			name := strings.TrimPrefix(ref[1], filepath.Base(directory)+".")
			if symbols[name] != filepath.Base(source) {
				t.Errorf("%s: %s is not declared in %s", path, ref[1], ref[2])
			}
		}
	}
	if checked == 0 {
		t.Fatal("no contract symbol references checked")
	}
}
