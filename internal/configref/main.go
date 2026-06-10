// configref generates docs/config-reference.md from the Go structs
// that define the .sparkwing/sparkwing.yaml schema (pkg/pipelines and
// pkg/projectconfig). The field set, yaml keys, types, and per-field
// godoc are read straight from source, so the config reference is
// derived from the same structs the strict parser enforces and cannot
// claim a field that doesn't exist (the exact rot that produced the
// runs_on / tags / env / secrets fiction).
//
// Usage: go run . <repo-root>   (writes markdown to stdout)
package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
)

var fset = token.NewFileSet()

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: configref <repo-root>")
		os.Exit(2)
	}
	root := os.Args[1]
	pf := mustParse(filepath.Join(root, "pkg", "pipelines", "pipelines.go"))
	cf := mustParse(filepath.Join(root, "pkg", "projectconfig", "projectconfig.go"))

	var b strings.Builder
	b.WriteString("<!-- GENERATED from the sparkwing.yaml schema structs (pkg/pipelines, pkg/projectconfig) by internal/configref. Do not edit by hand; regenerate with `bash bin/gen-config-docs.sh`. -->\n")
	// Field godoc is authored as prose and may start a line with `#`
	// or a list marker; disable the shape rules so schema wording never
	// has to satisfy markdownlint in this derived file.
	b.WriteString("<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->\n")
	b.WriteString("# Config reference\n\n")
	b.WriteString("The complete `.sparkwing/sparkwing.yaml` schema, generated from the " +
		"Go structs the strict config parser enforces. **Any key not listed here is " +
		"rejected at load.** `Required` reflects whether the field may be omitted.\n\n")

	section(&b, "Top level", structFields(cf, "Config"))
	section(&b, "`defaults`", structFields(cf, "Defaults"))
	section(&b, "Pipeline entry (a `pipelines:` list item)", structFields(pf, "Pipeline"))
	section(&b, "`guards`", structFields(pf, "Guards"))
	section(&b, "Triggers (`on:`)", structFields(pf, "Triggers"))
	section(&b, "`on.push`", structFields(pf, "PushTrigger"))
	section(&b, "`on.webhook`", structFields(pf, "WebhookTrigger"))

	fmt.Print(b.String())
}

func mustParse(path string) *ast.File {
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		fmt.Fprintln(os.Stderr, "configref parse:", err)
		os.Exit(2)
	}
	return f
}

type fieldDoc struct {
	yaml     string
	typ      string
	doc      string
	required bool
}

// structFields returns the yaml-tagged fields of the named struct in
// declaration order, with their type and godoc. A field is "required"
// when its yaml tag lacks omitempty (matching the schema: name and
// entrypoint carry no omitempty, everything else does).
func structFields(f *ast.File, name string) []fieldDoc {
	var out []fieldDoc
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != name {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return true
		}
		for _, fld := range st.Fields.List {
			if fld.Tag == nil || len(fld.Names) == 0 {
				continue
			}
			tag := reflect.StructTag(strings.Trim(fld.Tag.Value, "`"))
			yt := tag.Get("yaml")
			if yt == "" || yt == "-" {
				continue
			}
			parts := strings.Split(yt, ",")
			doc := fld.Doc.Text()
			if doc == "" && fld.Comment != nil {
				doc = fld.Comment.Text()
			}
			out = append(out, fieldDoc{
				yaml:     parts[0],
				typ:      typeString(fld.Type),
				doc:      doc,
				required: !slices.Contains(parts[1:], "omitempty"),
			})
		}
		return false
	})
	return out
}

// typeString renders a field's AST type as Go source, dropping a
// leading pointer star (the yaml shape doesn't care).
func typeString(e ast.Expr) string {
	var b bytes.Buffer
	_ = printer.Fprint(&b, fset, e)
	return strings.TrimPrefix(b.String(), "*")
}

func section(b *strings.Builder, title string, fields []fieldDoc) {
	if len(fields) == 0 {
		return
	}
	b.WriteString("## " + title + "\n\n")
	b.WriteString("| Field | Type | Required | Description |\n|---|---|---|---|\n")
	for _, f := range fields {
		req := "no"
		if f.required {
			req = "**yes**"
		}
		b.WriteString("| `" + f.yaml + "` | `" + f.typ + "` | " + req + " | " + cell(f.doc) + " |\n")
	}
	b.WriteString("\n")
}

// cell flattens prose for a markdown table cell: collapse whitespace
// and escape pipes.
func cell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.Join(strings.Fields(s), " ")
}
