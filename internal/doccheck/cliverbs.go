package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// registryPathRE captures the full command path of every Command literal
// in the help registry (`Path: "sparkwing ..."`). The registry is the
// source of truth for the user-facing command tree; help output,
// completion, and dispatch all read from it.
var registryPathRE = regexp.MustCompile(`Path:\s*"(sparkwing[^"]*)"`)

// posArgsFieldRE marks a Command that declares a positional argument. A
// command with a PosArg (e.g. `run <pipeline>`) swallows the next token
// as a value, so a bare word there is data, not a typo'd verb.
var posArgsFieldRE = regexp.MustCompile(`^\s*PosArgs:`)

// subcommandRefRE captures a SubcommandRef's leaf name (`{"xrepo",
// "..."}`). SubcommandRefs enumerate a group's children; some children
// (xrepo, whose own subcommands are handled in their handler) have no
// dedicated Path literal, so the ref is the only registry record that
// the group exists. The leaf name is always a bare lowercase word, which
// distinguishes these from Example entries (a capitalized description
// string) and FlagSpec/PosArg entries (`{Name: ...}`).
var subcommandRefRE = regexp.MustCompile(`^\s*\{"([a-z][a-z0-9-]*)",\s*"`)

// hiddenTopLevel are verbs the CLI dispatches but deliberately keeps out
// of the help surface: the per-node execution entrypoints that the
// runner and worker spawn as child processes. Architecture/internals docs
// legitimately name them, so they resolve here even though no Path lists
// them.
var hiddenTopLevel = []string{"run-node", "handle-trigger"}

// cliVerb is one `sparkwing ...` invocation lifted from the docs.
type cliVerb struct {
	file   string
	line   int
	tokens []string
	raw    string
}

// shellLangs are the fenced-block languages whose lines are shell
// commands (a no-language fence is commonly shell in these docs). Go,
// yaml, and json blocks are handled by other gates and never carry a bare
// `sparkwing` invocation to resolve.
var shellLangs = map[string]bool{
	"":        true,
	"bash":    true,
	"sh":      true,
	"shell":   true,
	"console": true,
	"text":    true,
}

// unshippedDesignRE detects a doc that declares itself a design sketch
// for an unshipped feature (a top-of-file status banner). Such a doc
// intentionally names commands that don't exist yet, so it is exempt from
// verb resolution; when the feature ships and the banner comes off, the
// doc is checked like any other.
var unshippedDesignRE = regexp.MustCompile(`(?i)not yet shipped|STATUS:\s*design`)

// checkCLIVerbs resolves every `sparkwing <verb> <subverb> ...`
// invocation shown in the docs against the CLI command tree and fails on
// any invocation naming a subcommand that doesn't exist -- one that was
// renamed or never existed. Flags, positional values, and comment tails
// end the path walk; only the leading run of subcommand words is
// resolved, so `sparkwing run my-pipeline --sw-dry-run` checks `run` and
// stops at the pipeline name. Returns false on any drift.
func checkCLIVerbs(contentDir, repoRoot string) bool {
	valid, posArgs, err := loadRegistry(repoRoot)
	if err != nil {
		fmt.Println("cli-verbs: load registry:", err)
		return false
	}

	var invocations []cliVerb
	_ = filepath.Walk(contentDir, func(path string, info os.FileInfo, werr error) error {
		if werr != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
			return werr
		}
		if strings.Contains(path, "/migrations/") || strings.Contains(path, "/proposals/") {
			return nil
		}
		if generatedDocs[filepath.Base(path)] {
			return nil
		}
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

// loadRegistry reads the help registry and returns the set of valid
// command paths (Path literals, SubcommandRef children, and the hidden
// execution verbs) plus the subset of paths that accept a positional
// argument.
func loadRegistry(repoRoot string) (valid, posArgs map[string]bool, err error) {
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
		if m := subcommandRefRE.FindStringSubmatch(line); m != nil {
			valid[lastPath+" "+m[1]] = true
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

// extractInvocations pulls candidate `sparkwing ...` commands out of a
// markdown document: the command at the start of each line inside a
// shell-ish fenced block, and each inline code span that is itself a
// `sparkwing` command. Requiring the invocation to start the line/span
// keeps prose that merely mentions the word (a numbered "sparkwing
// resolves the profile" flow step, a `# comment` tail) from being parsed
// as a command.
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

// subcmdTokenRE matches a bare lowercase word: the only shape a
// subcommand token can take. Flags, placeholders (<pipeline>), variables,
// and paths never match, so the path walk stops at them.
var subcmdTokenRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// parseInvocation reads a single shell line or inline span and, when it
// starts with the `sparkwing` command word (optionally behind a `$`/`#`
// prompt), returns the run of subcommand tokens that follow. Tokens after
// a shell operator or comment are dropped.
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

// resolvePath walks the subcommand tokens against the valid command
// paths. It returns "" when the invocation resolves (or terminates at a
// flag, a positional value, or a leaf command) and the offending token
// when a bare word names a subcommand that doesn't exist under an
// existing command group.
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

// isGroup reports whether cur has at least one child command in the
// registry, which makes an unknown next token a real drift rather than a
// positional value.
func isGroup(cur string, valid map[string]bool) bool {
	prefix := cur + " "
	for p := range valid {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}
