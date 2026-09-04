package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const goTypeKey = "x-sparkwing-go-type"

const goTypeNone = "none"

type goField struct {
	json string
	typ  string
}

type goTypes struct {
	root   string
	loaded map[string]map[string]goStruct
}

type goStruct struct {
	fields []goField
	err    error
}

func newGoTypes(root string) *goTypes {
	return &goTypes{root: root, loaded: map[string]map[string]goStruct{}}
}

func (g *goTypes) fields(qualified string) ([]goField, error) {
	cut := strings.LastIndex(qualified, ".")
	if cut <= 0 || cut == len(qualified)-1 {
		return nil, fmt.Errorf("%q is not <package-dir>.<TypeName>", qualified)
	}
	dir, name := qualified[:cut], qualified[cut+1:]
	pkg, err := g.load(dir)
	if err != nil {
		return nil, err
	}
	st, ok := pkg[name]
	if !ok {
		return nil, fmt.Errorf("%s declares no struct type %s", dir, name)
	}
	return st.fields, st.err
}

func (g *goTypes) load(dir string) (map[string]goStruct, error) {
	if pkg, ok := g.loaded[dir]; ok {
		return pkg, nil
	}
	abs := filepath.Join(g.root, filepath.FromSlash(dir))
	if _, err := os.Stat(abs); err != nil {
		return nil, fmt.Errorf("package dir %s: %w", dir, err)
	}
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, abs, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", dir, err)
	}
	structs := map[string]goStruct{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || gen.Tok != token.TYPE {
					continue
				}
				for _, spec := range gen.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					st, ok := ts.Type.(*ast.StructType)
					if !ok {
						continue
					}
					fields, err := structFields(st)
					structs[ts.Name.Name] = goStruct{fields: fields, err: err}
				}
			}
		}
	}
	g.loaded[dir] = structs
	return structs, nil
}

func structFields(st *ast.StructType) ([]goField, error) {
	var fields []goField
	for _, f := range st.Fields.List {
		tag := ""
		if f.Tag != nil {
			tag = reflect.StructTag(strings.Trim(f.Tag.Value, "`")).Get("json")
		}
		tagged, _, _ := strings.Cut(tag, ",")
		if tagged == "-" {
			continue
		}
		if len(f.Names) == 0 {
			return nil, fmt.Errorf("embedded field %s is not supported by the schema check", types.ExprString(f.Type))
		}
		for _, n := range f.Names {
			name := tagged
			if name == "" {
				if !n.IsExported() {
					continue
				}
				name = n.Name
			}
			fields = append(fields, goField{json: name, typ: types.ExprString(f.Type)})
		}
	}
	return fields, nil
}

var integerTypes = map[string]bool{
	"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
	"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true,
}

// safety: "" means the mapping is not one-to-one, so only the member name is checked
func schemaKind(goType string) string {
	t := strings.TrimPrefix(goType, "*")
	switch t {
	case "string":
		return "string"
	case "bool":
		return "boolean"
	case "float32", "float64":
		return "number"
	case "time.Time":
		return "string"
	case "[]byte":
		return "string"
	}
	switch {
	case integerTypes[t]:
		return "integer"
	case strings.HasPrefix(t, "[]"):
		return "array"
	case strings.HasPrefix(t, "map["):
		return "object"
	}
	return ""
}

func checkSchemaDrift(spec, repoRoot string) error {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(stripHeader(spec)), &doc); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("want a single YAML mapping document")
	}
	problems := checkSchemas(doc.Content[0], repoRoot)
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%d schema member(s) disagree with the Go types they document:\n  %s",
		len(problems), strings.Join(problems, "\n  "))
}

func checkSchemas(root *yaml.Node, repoRoot string) []string {
	components := mapValue(root, "components")
	if components == nil {
		return []string{"no components mapping"}
	}
	schemas := mapValue(components, "schemas")
	if schemas == nil || schemas.Kind != yaml.MappingNode {
		return []string{"no components.schemas mapping"}
	}
	c := &schemaChecker{types: newGoTypes(repoRoot)}
	for i := 0; i+1 < len(schemas.Content); i += 2 {
		c.walk(schemas.Content[i].Value, schemas.Content[i+1])
	}
	sort.Strings(c.problems)
	return c.problems
}

type schemaChecker struct {
	types    *goTypes
	problems []string
}

func (c *schemaChecker) reportf(where, format string, args ...any) {
	c.problems = append(c.problems, where+": "+fmt.Sprintf(format, args...))
}

func (c *schemaChecker) walk(where string, node *yaml.Node) {
	if node == nil || node.Kind != yaml.MappingNode {
		return
	}
	if props := mapValue(node, "properties"); props != nil && props.Kind == yaml.MappingNode {
		c.compare(where, node, props)
		for i := 0; i+1 < len(props.Content); i += 2 {
			c.walk(where+"."+props.Content[i].Value, props.Content[i+1])
		}
	}
	if items := mapValue(node, "items"); items != nil {
		c.walk(where+"[]", items)
	}
}

func (c *schemaChecker) compare(where string, node, props *yaml.Node) {
	named := mapValue(node, goTypeKey)
	if named == nil || named.Value == "" {
		c.reportf(where, "object has properties but no %s; name the Go type it mirrors, or %q where the controller builds it by hand",
			goTypeKey, goTypeNone)
		return
	}
	if named.Value == goTypeNone {
		return
	}
	fields, err := c.types.fields(named.Value)
	if err != nil {
		c.reportf(where, "%s %s: %v", goTypeKey, named.Value, err)
		return
	}
	byName := map[string]goField{}
	for _, f := range fields {
		byName[f.json] = f
	}
	documented := map[string]bool{}
	for i := 0; i+1 < len(props.Content); i += 2 {
		name, member := props.Content[i].Value, props.Content[i+1]
		documented[name] = true
		field, ok := byName[name]
		if !ok {
			c.reportf(where, "documents %q, which %s does not serialize", name, named.Value)
			continue
		}
		want := schemaKind(field.typ)
		if want == "" || member.Kind != yaml.MappingNode {
			continue
		}
		got := mapValue(member, "type")
		if got == nil {
			continue
		}
		if got.Value != want {
			c.reportf(where, "documents %q as %s; %s serializes %s, which is %s",
				name, got.Value, named.Value, field.typ, want)
		}
	}
	for _, f := range fields {
		if !documented[f.json] {
			c.reportf(where, "omits %q, which %s serializes", f.json, named.Value)
		}
	}
}
