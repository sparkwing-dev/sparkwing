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
	for _, name := range []string{"settings", "nodehost", "store"} {
		packages[name] = contractSymbols(t, root+"internal/"+name)
	}
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
	data, err = os.ReadFile(root + "design/rewrite/contracts/c3-protocol-sdk.md")
	if err != nil {
		t.Fatal(err)
	}
	_, inventory, ok := strings.Cut(string(data), "## Host and store export inventory\n")
	if !ok {
		t.Fatal("missing host inventory")
	}
	inventory, _, _ = strings.Cut(inventory, "## Lease and source export inventory")
	quoted := regexp.MustCompile("`([^`]+)`")
	for _, line := range strings.Split(inventory, "\n") {
		cells := strings.Split(line, "|")
		if len(cells) < 3 {
			continue
		}
		owner := ""
		for _, match := range quoted.FindAllStringSubmatch(cells[1], -1) {
			symbol := match[1]
			if prefix, name, found := strings.Cut(symbol, "."); found && packages[prefix] != nil {
				owner, symbol = prefix, name
			}
			if owner == "" || packages[owner][symbol] == "" {
				t.Errorf("host inventory names missing symbol %s.%s", owner, symbol)
			}
		}
	}
}
