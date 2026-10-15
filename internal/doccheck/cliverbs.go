package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
)

type cliVerb struct {
	file   string
	line   int
	tokens []string
	raw    string
	args   []string
	err    error
}

var shellLangs = map[string]bool{
	"":        true,
	"bash":    true,
	"sh":      true,
	"shell":   true,
	"console": true,
	"text":    true,
}

func checkCLIVerbs(contentDir string) bool {
	valid, posArgs := loadRegistry()

	var invocations []cliVerb
	// #nosec G703 -- a build-time tool reading paths the operator names
	err := filepath.Walk(contentDir, func(path string, info os.FileInfo, werr error) error {
		if werr != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
			return werr
		}
		if strings.Contains(path, "/migrations/") {
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
		rel, _ := filepath.Rel(contentDir, path)
		invocations = append(invocations, extractInvocations(rel, string(data))...)
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "doccheck/cli-verbs:", err)
		return false
	}

	var bad []string
	for _, inv := range invocations {
		if inv.args != nil || inv.err != nil {
			err := inv.err
			if err == nil {
				_, err = clisurface.Parse("sparkwing", inv.args)
			}
			if err != nil {
				bad = append(bad, fmt.Sprintf("%s:%d: `%s` -- %v", inv.file, inv.line, inv.raw, err))
			}
		} else if unknown := resolvePath(inv.tokens, valid, posArgs); unknown != "" {
			bad = append(bad, fmt.Sprintf("%s:%d: `%s` -- no such command %q", inv.file, inv.line, inv.raw, unknown))
		}
	}

	fmt.Printf("doccheck/cli-verbs: %d sparkwing invocation(s) in docs -- %d unresolved\n", len(invocations), len(bad))
	if len(bad) > 0 {
		fmt.Printf("\n%d doc invocation(s) outside the CLI grammar:\n", len(bad))
		for _, b := range bad {
			fmt.Println("  " + b)
		}
		return false
	}
	fmt.Println("\nALL sparkwing INVOCATIONS RESOLVE")
	return true
}

func loadRegistry() (valid, posArgs map[string]bool) {
	valid, posArgs = map[string]bool{}, map[string]bool{}
	for _, command := range clisurface.Registry() {
		valid[command.Path] = true
		posArgs[command.Path] = command.PipelineArgument
	}
	return valid, posArgs
}

var inlineCodeRE = regexp.MustCompile("`([^`]+)`")

func extractInvocations(file, doc string) []cliVerb {
	var out []cliVerb
	lines := strings.Split(doc, "\n")
	inFence := false
	fenceShell := false
	complete := false
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if inFence {
				inFence = false
				continue
			}
			inFence = true
			lang := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(trimmed, "```")))
			fenceShell = shellLangs[lang]
			complete = fenceShell && lang != "text"
			continue
		}
		if inFence {
			if !fenceShell {
				continue
			}
			start := i
			if complete {
				for strings.HasSuffix(strings.TrimRight(line, " \t"), "\\") && i+1 < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i+1]), "```") {
					i++
					line = strings.TrimRight(line, " \t") + "\n" + lines[i]
				}
			}
			if inv, ok := parseInvocation(file, start+1, line); ok {
				if complete {
					inv.args, inv.err = shellExampleArgs(line)
				}
				out = append(out, inv)
			}
			continue
		}
		start := i
		for strings.Count(line, "`")%2 != 0 && i+1 < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i+1]), "```") {
			i++
			line += "\n" + lines[i]
		}
		for _, m := range inlineCodeRE.FindAllStringSubmatch(line, -1) {
			if inv, ok := parseInvocation(file, start+1, m[1]); ok {
				args, err := shellExampleArgs(m[1])
				if err == nil {
					_, err = clisurface.Parse("sparkwing", append([]string{"--help"}, args...))
				}
				var usage *clisurface.UsageError
				if errors.As(err, &usage) && (strings.HasPrefix(usage.Message, "unknown flag ") || strings.HasPrefix(usage.Message, "unknown short flag ") || strings.HasSuffix(usage.Message, " is not accepted")) {
					inv.err = err
				}
				out = append(out, inv)
			}
		}
	}
	return out
}

func shellExampleArgs(line string) ([]string, error) {
	line = strings.TrimSpace(line)
	line = strings.TrimSpace(strings.TrimPrefix(line, "$ "))
	var words []string
	var word strings.Builder
	var quote rune
	started, escaped, placeholder := false, false, false
	flush := func() {
		if started {
			words = append(words, word.String())
			word.Reset()
			started = false
		}
	}
	for i, r := range line {
		if escaped {
			if r != '\n' {
				word.WriteRune(r)
				started = true
			}
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			continue
		}
		switch r {
		case '\'', '"':
			quote, started = r, true
		case ' ', '\t', '\n', '\r':
			flush()
		case '#':
			if !started {
				return words[1:], nil
			}
			word.WriteRune(r)
		case '|', '&', ';':
			flush()
			return words[1:], nil
		case '<', '>':
			if r == '<' && !started {
				end := strings.IndexByte(line[i+1:], '>')
				placeholder = end > 0 && !strings.ContainsAny(line[i+1:i+1+end], " \t\r\n<")
			}
			if placeholder {
				word.WriteRune(r)
				started = true
				if r == '>' {
					placeholder = false
				}
				continue
			}
			if word.String() != "2" {
				flush()
			}
			return words[1:], nil
		default:
			word.WriteRune(r)
			started = true
		}
	}
	if quote != 0 || escaped {
		return nil, fmt.Errorf("unterminated shell quote or continuation")
	}
	flush()
	return words[1:], nil
}

var rootValueFlags = map[string]bool{"-C": true, "--profile": true, "-o": true, "--output": true}

var subcmdTokenRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

func parseInvocation(file string, line int, s string) (cliVerb, bool) {
	fields := strings.Fields(strings.TrimSpace(s))
	if len(fields) > 0 && fields[0] == "$" {
		fields = fields[1:]
	}
	if len(fields) == 0 || fields[0] != "sparkwing" {
		return cliVerb{}, false
	}
	rest := fields[1:]
	for len(rest) > 1 && rootValueFlags[rest[0]] {
		rest = rest[2:]
	}
	var tokens []string
	for _, f := range rest {
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
