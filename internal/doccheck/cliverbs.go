package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
		if isGeneratedDoc(path) {
			return nil
		}
		// #nosec G122 -- a TOCTOU swap here needs write access to the checkout this build-time check already trusts
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		rel, _ := filepath.Rel(contentDir, path)
		doc := string(data)
		if strings.Contains(filepath.ToSlash(rel), "migrations/") {
			doc = migrationDestinations(doc)
		}
		doc = normalizeUnprefixedInvocations(doc)
		examples := extractInvocations(rel, doc)
		if strings.Contains(filepath.ToSlash(rel), "migrations/") {
			for i := range examples {
				if examples[i].args != nil || examples[i].err != nil || valid[examples[i].raw] {
					continue
				}
				raw := strings.NewReplacer("[flags]", "", "[-- pipeline-flags]", "", "[", "", "]", "", "...", "").Replace(examples[i].raw)
				examples[i].args, examples[i].err = shellExampleArgs(raw)
			}
		}
		invocations = append(invocations, examples...)
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "doccheck/cli-verbs:", err)
		return false
	}

	var bad []string
	for _, inv := range invocations {
		if problem := judgeInvocation(inv, valid, posArgs); problem != "" {
			bad = append(bad, problem)
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

func judgeInvocation(inv cliVerb, valid, posArgs map[string]bool) string {
	if inv.args != nil || inv.err != nil {
		err := inv.err
		if err == nil {
			_, err = clisurface.Parse("sparkwing", inv.args)
		}
		if err != nil {
			return fmt.Sprintf("%s:%d: `%s` -- %v", inv.file, inv.line, inv.raw, err)
		}
		return ""
	}
	if unknown := resolvePath(inv.tokens, valid, posArgs); unknown != "" {
		return fmt.Sprintf("%s:%d: `%s` -- no such command %q", inv.file, inv.line, inv.raw, unknown)
	}
	return ""
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
				inv.err = fragmentFlagError(m[1], true)
				out = append(out, inv)
			}
		}
	}
	return out
}

// hack: --help skips required-flag checks, so fragments that omit required values still pass.
// hack: a markdown fragment is copied as written, so a stray positional fails
// it; a Go source hint sits in prose, where a positional is only noise.
func fragmentFlagError(command string, positional bool) error {
	args, err := shellExampleArgs(command)
	if err == nil {
		args = slices.DeleteFunc(args, func(word string) bool {
			return word == "..." || strings.HasPrefix(word, "[") && strings.HasSuffix(word, "]")
		})
		_, err = clisurface.Parse("sparkwing", append([]string{"--help"}, args...))
	}
	var usage *clisurface.UsageError
	if errors.As(err, &usage) && (strings.HasPrefix(usage.Message, "unknown flag ") || strings.HasPrefix(usage.Message, "unknown short flag ") || strings.HasSuffix(usage.Message, " is not accepted") || (positional && strings.HasPrefix(usage.Message, "unexpected positional argument"))) {
		return err
	}
	return nil
}

var envAssignmentRE = regexp.MustCompile(`^(?:[A-Za-z_][A-Za-z0-9_]*=(?:"[^"]*"|'[^']*'|[^\s"']*)[ \t]+)+`)

func stripEnvAssignments(line string) string {
	line = strings.TrimSpace(line)
	line = strings.TrimSpace(strings.TrimPrefix(line, "$ "))
	return envAssignmentRE.ReplaceAllString(line, "")
}

func shellExampleArgs(line string) ([]string, error) {
	line = stripEnvAssignments(line)
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
	fields := strings.Fields(stripEnvAssignments(s))
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

func normalizeUnprefixedInvocations(doc string) string {
	roots := map[string]bool{}
	for _, command := range clisurface.Registry() {
		words := strings.Fields(command.Path)
		if len(words) > 1 && words[0] == "sparkwing" {
			roots[words[1]] = true
		}
	}
	for _, root := range strings.Fields("cloud configure cluster serve crons daemon profile") {
		roots[root] = true
	}
	normalize := func(example string) string {
		command := strings.TrimLeft(example, " \t")
		if strings.HasPrefix(command, "$ ") || strings.HasPrefix(command, "$\t") {
			command = strings.TrimLeft(command[1:], " \t")
		}
		words := strings.Fields(command)
		if len(words) > 1 && roots[words[0]] {
			return "sparkwing " + command
		}
		return example
	}
	lines := strings.Split(doc, "\n")
	proseStart := 0
	prose := func(end int) {
		chunk := inlineCodeRE.ReplaceAllStringFunc(strings.Join(lines[proseStart:end], "\n"), func(snippet string) string {
			return "`" + normalize(strings.Trim(snippet, "`")) + "`"
		})
		copy(lines[proseStart:end], strings.Split(chunk, "\n"))
	}
	fence, shell := false, false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if fence {
				proseStart = i + 1
			} else {
				prose(i)
				shell = shellLangs[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(trimmed, "```")))]
			}
			fence = !fence
		} else if fence && shell {
			lines[i] = normalize(line)
		}
	}
	if !fence {
		prose(len(lines))
	}
	return strings.Join(lines, "\n")
}

func migrationDestinations(doc string) string {
	historical := regexp.MustCompile("Before: `[^`]*`")
	lines := strings.Split(doc, "\n")
	before, fence, column, depth, ended := false, false, -1, 0, false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !fence {
			heading := len(trimmed) - len(strings.TrimLeft(trimmed, "#"))
			if heading > 0 && depth > 0 && heading <= depth {
				depth = 0
				before = false
			}
			if depth > 0 {
				lines[i] = ""
				if strings.HasPrefix(trimmed, "```") {
					fence = true
				}
				continue
			}
			if strings.HasPrefix(trimmed, "|") {
				cells := strings.Split(strings.ReplaceAll(trimmed, `\|`, "\x00"), "|")
				for j, cell := range cells {
					if strings.TrimSpace(cell) == "Before" {
						column = j
					}
				}
				if column >= 0 && column < len(cells) {
					cells[column] = ""
					lines[i] = strings.ReplaceAll(strings.Join(cells, "|"), "\x00", "|")
				}
				continue
			}
			column = -1
			if before && ended && trimmed != "" && line == trimmed && !strings.HasPrefix(trimmed, "```") {
				before = false
			}
			label := strings.TrimLeft(trimmed, "# *-")
			if strings.HasPrefix(label, "Before:") || strings.HasPrefix(label, "Before**") || label == "Before" {
				before = true
				depth = heading
				ended = false
				if heading == 0 && strings.HasPrefix(trimmed, "Before:") && historical.MatchString(trimmed) {
					before = false
				}
			} else if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "- **") || strings.HasPrefix(trimmed, "**") || strings.HasSuffix(trimmed, ":") {
				before = false
			}
		}
		if before && !fence && trimmed == "" {
			ended = true
		}
		if strings.HasPrefix(trimmed, "```") {
			fence = !fence
		}
		if before {
			lines[i] = ""
		} else {
			lines[i] = historical.ReplaceAllString(lines[i], "")
		}
	}
	return strings.Join(lines, "\n")
}
