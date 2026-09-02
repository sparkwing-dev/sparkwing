package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var registryPathRE = regexp.MustCompile(`Path:\s*"(sparkwing[^"]*)"`)

var posArgsFieldRE = regexp.MustCompile(`^\s*PosArgs:`)

var hiddenTopLevel = []string{"run-node", "handle-trigger"}

type cliVerb struct {
	file   string
	line   int
	tokens []string
	raw    string
}

var shellLangs = map[string]bool{
	"":        true,
	"bash":    true,
	"sh":      true,
	"shell":   true,
	"console": true,
	"text":    true,
}

var unshippedDesignRE = regexp.MustCompile(`(?i)not yet shipped|STATUS:\s*design`)

func checkCLIVerbs(contentDir, repoRoot string) bool {
	valid, posArgs, err := loadRegistry(repoRoot)
	if err != nil {
		fmt.Println("cli-verbs: load registry:", err)
		return false
	}

	var invocations []cliVerb
	// #nosec G703 -- a build-time tool reading paths the operator names
	_ = filepath.Walk(contentDir, func(path string, info os.FileInfo, werr error) error {
		if werr != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
			return werr
		}
		if strings.Contains(path, "/migrations/") || strings.Contains(path, "/proposals/") {
			return nil
		}
		if isGeneratedDoc(path) {
			return nil
		}
		// #nosec G122 -- a TOCTOU swap here needs write access to the checkout this build-time check already trusts
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if unshippedDesignRE.Match(data) {
			return nil
		}
		rel, _ := filepath.Rel(contentDir, path)
		invocations = append(invocations, extractInvocations(rel, string(data))...)
		return nil
	})

	var bad []string
	for _, inv := range invocations {
		if unknown := resolvePath(inv.tokens, valid, posArgs); unknown != "" {
			bad = append(bad, fmt.Sprintf("%s:%d: `%s` -- no such command %q", inv.file, inv.line, inv.raw, unknown))
		}
	}

	fmt.Printf("doccheck/cli-verbs: %d sparkwing invocation(s) in docs -- %d unresolved\n", len(invocations), len(bad))
	if len(bad) > 0 {
		fmt.Printf("\n%d doc invocation(s) naming a verb the CLI doesn't have:\n", len(bad))
		for _, b := range bad {
			fmt.Println("  " + b)
		}
		return false
	}
	fmt.Println("\nALL sparkwing INVOCATIONS RESOLVE")
	return true
}

func loadRegistry(repoRoot string) (valid, posArgs map[string]bool, err error) {
	// #nosec G703 -- a build-time tool reading paths the operator names
	data, err := os.ReadFile(filepath.Join(repoRoot, "cmd", "sparkwing", "help_registry.go"))
	if err != nil {
		return nil, nil, err
	}
	valid = map[string]bool{}
	posArgs = map[string]bool{}
	var lastPath string
	for _, line := range strings.Split(string(data), "\n") {
		if m := registryPathRE.FindStringSubmatch(line); m != nil {
			lastPath = m[1]
			valid[lastPath] = true
			continue
		}
		if lastPath == "" {
			continue
		}
		if posArgsFieldRE.MatchString(line) {
			posArgs[lastPath] = true
			continue
		}
	}
	if len(valid) == 0 {
		return nil, nil, fmt.Errorf("no Command paths found in help_registry.go")
	}
	for _, v := range hiddenTopLevel {
		valid["sparkwing "+v] = true
	}
	return valid, posArgs, nil
}

var inlineCodeRE = regexp.MustCompile("`([^`]+)`")

func extractInvocations(file, doc string) []cliVerb {
	var out []cliVerb
	lines := strings.Split(doc, "\n")
	inFence := false
	fenceShell := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if inFence {
				inFence = false
				continue
			}
			inFence = true
			lang := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(trimmed, "```")))
			fenceShell = shellLangs[lang]
			continue
		}
		if inFence {
			if !fenceShell {
				continue
			}
			if inv, ok := parseInvocation(file, i+1, line); ok {
				out = append(out, inv)
			}
			continue
		}
		for _, m := range inlineCodeRE.FindAllStringSubmatch(line, -1) {
			if inv, ok := parseInvocation(file, i+1, m[1]); ok {
				out = append(out, inv)
			}
		}
	}
	return out
}

var subcmdTokenRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

func parseInvocation(file string, line int, s string) (cliVerb, bool) {
	fields := strings.Fields(strings.TrimSpace(s))
	if len(fields) > 0 && (fields[0] == "$" || fields[0] == "#") {
		fields = fields[1:]
	}
	if len(fields) == 0 || fields[0] != "sparkwing" {
		return cliVerb{}, false
	}
	var tokens []string
	for _, f := range fields[1:] {
		if isShellOperator(f) || strings.HasPrefix(f, "#") {
			break
		}
		tokens = append(tokens, f)
	}
	raw := strings.Join(append([]string{"sparkwing"}, tokens...), " ")
	return cliVerb{file: file, line: line, tokens: tokens, raw: raw}, true
}

func isShellOperator(tok string) bool {
	switch tok {
	case "|", "||", "&&", ";", "&", ">", ">>", "<", "2>", "2>&1":
		return true
	}
	return false
}

func resolvePath(tokens []string, valid, posArgs map[string]bool) string {
	cur := "sparkwing"
	for _, t := range tokens {
		if !subcmdTokenRE.MatchString(t) {
			return ""
		}
		candidate := cur + " " + t
		if valid[candidate] {
			cur = candidate
			continue
		}
		if posArgs[cur] {
			return ""
		}
		if isGroup(cur, valid) {
			return t
		}
		return ""
	}
	return ""
}

func isGroup(cur string, valid map[string]bool) bool {
	prefix := cur + " "
	for p := range valid {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}
