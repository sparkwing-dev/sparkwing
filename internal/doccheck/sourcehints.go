package main

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type sourceHint struct {
	cliVerb
	// hack: bare prose such as "sparkwing repos and their pins" names no subcommand.
	checkPath bool
}

var sourceHintRE = regexp.MustCompile("(^|[^\\w/.-])sparkwing ([a-z][^`\n,;()\"]*)")

func checkSourceHints(repoRoot string) bool {
	valid, posArgs := loadRegistry()
	var hints []sourceHint
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
			found, perr := sourceHintsIn(repoRoot, path, valid)
			hints = append(hints, found...)
			return perr
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "doccheck/source-hints:", err)
			return false
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

func sourceHintsIn(repoRoot, path string, valid map[string]bool) ([]sourceHint, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(repoRoot, path)
	if err != nil {
		return nil, err
	}
	var hints []sourceHint
	ast.Inspect(file, func(node ast.Node) bool {
		lit, ok := node.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		text, err := strconv.Unquote(lit.Value)
		if err != nil {
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
			verb, _ := parseInvocation(rel, fset.Position(lit.Pos()).Line, command)
			if len(verb.tokens) == 0 || !valid["sparkwing "+verb.tokens[0]] {
				continue
			}
			verb.err = fragmentFlagError(command)
			hints = append(hints, sourceHint{cliVerb: verb, checkPath: quoted || strings.Contains(command, " -")})
		}
		return true
	})
	return hints, nil
}
