package main

import (
	"errors"
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

type sourceHint struct {
	cliVerb
	// hack: bare prose such as "sparkwing repos and their pins" names no subcommand.
	checkPath bool
}

var sourceHintRE = regexp.MustCompile("(^|[^\\w/.-])sparkwing ([a-z][^`\n,;()\"]*)")

var (
	sourceHintFlagRE        = regexp.MustCompile(`(?:^| )-{1,2}[a-zA-Z]`)
	sourceHintInstructionRE = regexp.MustCompile(`(?i)\b(?:run|use|try|execute) sparkwing `)
)

func checkSourceHints(repoRoot string) bool {
	valid, posArgs := loadRegistry()
	var hints []sourceHint
	fset := token.NewFileSet()
	packages := map[string][]*ast.File{}
	var directories []string
	for _, dir := range []string{"cmd", "internal", "pkg"} {
		// #nosec G703 -- a build-time tool reading paths the operator names
		err := filepath.WalkDir(filepath.Join(repoRoot, dir), func(path string, d fs.DirEntry, werr error) error {
			if errors.Is(werr, fs.ErrNotExist) {
				return nil
			}
			if werr != nil {
				return werr
			}
			// hack: doccheck holds retired spellings as banned patterns.
			if d.IsDir() && (d.Name() == "testdata" || d.Name() == "doccheck") {
				return filepath.SkipDir
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			directory := filepath.Dir(path)
			if _, exists := packages[directory]; !exists {
				directories = append(directories, directory)
			}
			packages[directory] = append(packages[directory], file)
			return nil
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "doccheck/source-hints:", err)
			return false
		}
	}
	for _, directory := range directories {
		files := packages[directory]
		info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}}
		// hack: the scan includes platform variants and incomplete fixture packages.
		config := types.Config{Error: func(error) {}}
		_, _ = config.Check(files[0].Name.Name, fset, files, info)
		used := map[types.Object]bool{}
		for _, object := range info.Uses {
			used[object] = true
		}
		for _, file := range files {
			found, err := sourceHintsIn(repoRoot, fset, file, info, used)
			if err != nil {
				fmt.Fprintln(os.Stderr, "doccheck/source-hints:", err)
				return false
			}
			hints = append(hints, found...)
		}
	}

	var bad []string
	for _, hint := range hints {
		if hint.err != nil {
			bad = append(bad, fmt.Sprintf("%s:%d: `%s` -- %v", hint.file, hint.line, hint.raw, hint.err))
		} else if hint.checkPath {
			if unknown := resolvePath(hint.tokens, valid, posArgs); unknown != "" {
				bad = append(bad, fmt.Sprintf("%s:%d: `%s` -- no such command %q", hint.file, hint.line, hint.raw, unknown))
			}
		}
	}
	fmt.Printf("doccheck/source-hints: %d sparkwing hint(s) in Go source -- %d unresolved\n", len(hints), len(bad))
	for _, b := range bad {
		fmt.Printf("  %s\n", b)
	}
	return len(bad) == 0
}

func sourceHintsIn(repoRoot string, fset *token.FileSet, file *ast.File, info *types.Info, used map[types.Object]bool) ([]sourceHint, error) {
	rel, err := filepath.Rel(repoRoot, fset.Position(file.Pos()).Filename)
	if err != nil {
		return nil, err
	}
	var hints []sourceHint
	var stack []ast.Node
	ast.Inspect(file, func(node ast.Node) bool {
		if node == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		var parent ast.Node
		if len(stack) > 0 {
			parent = stack[len(stack)-1]
		}
		if declaration, ok := node.(*ast.ValueSpec); ok && slices.ContainsFunc(declaration.Names, func(name *ast.Ident) bool {
			_, constant := info.Defs[name].(*types.Const)
			return constant && used[info.Defs[name]]
		}) {
			return false
		}
		stack = append(stack, node)
		expression, ok := node.(ast.Expr)
		if !ok {
			return true
		}
		if _, nested := parent.(*ast.BinaryExpr); nested {
			return true
		}
		if _, nested := parent.(*ast.ParenExpr); nested {
			return true
		}
		text, ok := sourceHintExpression(expression, info)
		if !ok {
			return true
		}
		for _, m := range sourceHintRE.FindAllStringSubmatch(text, -1) {
			command := m[2]
			quoted := m[1] == "`"
			if !quoted {
				if i := strings.Index(command, ". "); i >= 0 {
					command = command[:i]
				}
			}
			command = "sparkwing " + strings.TrimRight(command, ".: ")
			verb, _ := parseInvocation(rel, fset.Position(expression.Pos()).Line, command)
			if len(verb.tokens) == 0 {
				continue
			}
			verb.err = fragmentFlagError(command)
			standalone := !sourceHintDataLiteral(parent, expression) && slices.ContainsFunc(strings.Split(text, "\n"), func(line string) bool {
				line = strings.TrimSpace(line)
				line = strings.TrimPrefix(line, "$ ")
				return line == command && !strings.ContainsRune(line, 0)
			})
			hints = append(hints, sourceHint{cliVerb: verb, checkPath: standalone || quoted || sourceHintFlagRE.MatchString(command) || sourceHintInstructionRE.MatchString(text)})
		}
		return true
	})
	return hints, nil
}

func sourceHintDataLiteral(parent ast.Node, literal ast.Expr) bool {
	switch parent := parent.(type) {
	case *ast.CallExpr:
		if array, ok := parent.Fun.(*ast.ArrayType); ok && array.Len == nil {
			return true
		}
		if function, ok := parent.Fun.(*ast.SelectorExpr); ok {
			if name, ok := function.X.(*ast.Ident); ok && name.Name == "strings" && function.Sel.Name == "NewReader" {
				return true
			}
		}
		for i, arg := range parent.Args {
			if arg == literal && i > 0 {
				if previous, ok := parent.Args[i-1].(*ast.BasicLit); ok && previous.Value == `"-m"` {
					return true
				}
			}
		}
	}
	return false
}

func sourceHintExpression(expression ast.Expr, info *types.Info) (string, bool) {
	if value := info.Types[expression].Value; value != nil && value.Kind() == constant.String {
		return constant.StringVal(value), true
	}
	switch expression := expression.(type) {
	case *ast.BasicLit:
		if expression.Kind == token.STRING {
			text, err := strconv.Unquote(expression.Value)
			return text, err == nil
		}
	case *ast.BinaryExpr:
		if expression.Op == token.ADD {
			left, leftOK := sourceHintExpression(expression.X, info)
			right, rightOK := sourceHintExpression(expression.Y, info)
			if !leftOK {
				left = "\x00"
			}
			if !rightOK {
				right = "\x00"
			}
			return left + right, leftOK || rightOK
		}
	case *ast.ParenExpr:
		return sourceHintExpression(expression.X, info)
	}
	return "", false
}
