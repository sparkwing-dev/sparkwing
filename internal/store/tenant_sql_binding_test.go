package store

import (
	"regexp"
	"slices"
	"strings"
)

// The same tokens decide row filters, inserted tenant values and conflict guards.
// Quoted text and comments cannot manufacture a column, clause or placeholder.
var scopeTokenRe = regexp.MustCompile(`(?s)--[^\n]*|/\*.*?\*/|'(?:''|[^'])*'|"(?:""|[^"])*"|` + "`(?:``|[^`])*`" + `|[a-zA-Z_$][a-zA-Z0-9_$]*|\S`)

func scopeTokens(sql string) []string {
	tokens := scopeTokenRe.FindAllString(strings.ToLower(sql), -1)
	return slices.DeleteFunc(tokens, func(token string) bool { return strings.HasPrefix(token, "--") || strings.HasPrefix(token, "/*") })
}

func scopeParts(tokens []string, separator string) [][]string {
	var parts [][]string
	depth, start := 0, 0
	for i, token := range tokens {
		switch token {
		case "(":
			depth++
		case ")":
			depth--
		default:
			if depth == 0 && token == separator {
				parts = append(parts, tokens[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, tokens[start:])
}

func scopeClose(tokens []string, start int) int {
	depth := 0
	for i := start; i < len(tokens); i++ {
		switch tokens[i] {
		case "(":
			depth++
		case ")":
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func scopeTeam(tokens []string) string {
	if len(tokens) >= 6 && tokens[0] == "coalesce" && tokens[1] == "(" && tokens[len(tokens)-1] == ")" && tokens[len(tokens)-2] == "''" && tokens[len(tokens)-3] == "," {
		return scopeTeam(tokens[2 : len(tokens)-3])
	}
	if len(tokens) == 1 && tokens[0] == "team" {
		return "team"
	}
	if len(tokens) == 3 && tokens[1] == "." && tokens[2] == "team" {
		return tokens[0] + ".team"
	}
	return ""
}

func scopeParameter(tokens []string) bool {
	if len(tokens) != 1 {
		return false
	}
	if tokens[0] == "?" {
		return true
	}
	token := tokens[0]
	if len(token) < 2 || token[0] != '$' || token[1] == '0' {
		return false
	}
	for _, c := range token[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func scopePredicate(tokens, parents []string) bool {
	for len(tokens) > 1 && tokens[0] == "(" && scopeClose(tokens, 0) == len(tokens)-1 {
		tokens = tokens[1 : len(tokens)-1]
	}
	if branches := scopeParts(tokens, "or"); len(branches) > 1 {
		return len(branches) > 0 && !slices.ContainsFunc(branches, func(branch []string) bool { return !scopePredicate(branch, parents) })
	}
	if branches := scopeParts(tokens, "and"); len(branches) > 1 {
		return slices.ContainsFunc(branches, func(branch []string) bool { return scopePredicate(branch, parents) })
	}
	if equal := scopeParts(tokens, "="); len(equal) == 2 {
		left, right := scopeTeam(equal[0]), scopeTeam(equal[1])
		return left != "" && scopeParameter(equal[1]) || right != "" && scopeParameter(equal[0]) || left != "" && right != "" && (slices.Contains(parents, left) || slices.Contains(parents, right))
	}
	if in := scopeParts(tokens, "in"); len(in) == 2 {
		values := in[1]
		if len(values) < 3 || values[0] != "(" || scopeClose(values, 0) != len(values)-1 {
			return false
		}

		return scopeTeam(in[0]) != "" && !slices.ContainsFunc(scopeParts(values[1:len(values)-1], ","), func(value []string) bool { return !scopeParameter(value) })
	}
	return false
}

func scopeRowFilter(tokens []string) []string {
	depth, start := 0, -1
	for i, token := range tokens {
		switch token {
		case "(":
			depth++
		case ")":
			depth--
		default:
			if depth != 0 {
				continue
			}
			if start < 0 {
				if token == "where" {
					start = i + 1
				}
				continue
			}
			if slices.Contains([]string{"group", "order", "limit", "returning", "union", "having", "on"}, token) {
				return tokens[start:i]
			}
		}
	}
	if start < 0 {
		return nil
	}
	return tokens[start:]
}

func scopeFilter(tokens, parents []string) bool {
	return scopePredicate(scopeRowFilter(tokens), parents)
}

func scopeInsert(tokens []string) (string, [][]string, []string) {
	into := slices.Index(tokens, "into")
	open := slices.Index(tokens, "(")
	if len(tokens) == 0 || tokens[0] != "insert" || into < 0 || open <= into+1 {
		return "", nil, nil
	}
	close := scopeClose(tokens, open)
	if close < 0 {
		return "", nil, nil
	}
	return tokens[into+1], scopeParts(tokens[open+1:close], ","), tokens[close+1:]
}

func scopeBound(tokens, parents []string) bool {
	if len(tokens) == 0 {
		return false
	}
	if branches := scopeParts(tokens, "union"); len(branches) > 1 {
		return !slices.ContainsFunc(branches, func(branch []string) bool {
			if len(branch) > 0 && (branch[0] == "all" || branch[0] == "distinct") {
				branch = branch[1:]
			}
			return len(tenantTablesTouchedBy(strings.Join(branch, " "))) > 0 && !scopeBound(branch, parents)
		})
	}
	if tokens[0] != "insert" {
		return scopeEveryRelationBound(tokens, parents, "team", teamColumnTables())
	}
	_, columns, tail := scopeInsert(tokens)
	team := -1
	for i, column := range columns {
		if len(column) == 1 && column[0] == "team" {
			team = i
		}
	}
	if team < 0 {
		return false
	}
	conflict := len(tail)
	for i := 0; i+1 < len(tail); i++ {
		if tail[i] == "on" && tail[i+1] == "conflict" {
			conflict = i
			break
		}
	}
	values := tail[:conflict]
	if len(values) == 0 {
		return false
	}
	switch values[0] {
	case "values":
		for _, row := range scopeParts(values[1:], ",") {
			if len(row) < 2 || row[0] != "(" || scopeClose(row, 0) != len(row)-1 {
				return false
			}
			fields := scopeParts(row[1:len(row)-1], ",")
			if team >= len(fields) || !scopeParameter(fields[team]) {
				return false
			}
		}
	case "select":
		projection := scopeParts(scopeParts(values[1:], "where")[0], "from")
		if len(projection) > 1 && !scopeEveryRelationBound(values, parents, "team", teamColumnTables()) {
			return false
		}
		fields := scopeParts(projection[0], ",")
		if team >= len(fields) || !scopeParameter(fields[team]) && scopeTeam(fields[team]) == "" {
			return false
		}
	default:
		return false
	}
	if conflict == len(tail) {
		return true
	}
	target := tail[conflict+2:]
	if len(target) > 0 && target[0] == "(" {
		end := scopeClose(target, 0)
		if end < 0 {
			return false
		}
		if slices.ContainsFunc(scopeParts(target[1:end], ","), func(column []string) bool { return len(column) == 1 && column[0] == "team" }) {
			return true
		}
	}
	return scopeFilter(target, []string{"excluded.team"})
}
