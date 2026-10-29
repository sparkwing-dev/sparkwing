package store

import (
	"maps"
	"slices"
)

// safety: a reference to column, bare or qualified, possibly inside COALESCE(ref,”); anything else is no binding.
func scopeColumnRef(tokens []string, column string) string {
	if len(tokens) >= 6 && tokens[0] == "coalesce" && tokens[1] == "(" && tokens[len(tokens)-1] == ")" && tokens[len(tokens)-2] == "''" && tokens[len(tokens)-3] == "," {
		return scopeColumnRef(tokens[2:len(tokens)-3], column)
	}
	if len(tokens) == 1 && tokens[0] == column {
		return column
	}
	if len(tokens) == 3 && tokens[1] == "." && tokens[2] == column {
		return tokens[0] + "." + column
	}
	return ""
}

// safety: the references a restricting predicate pins to a parameter, directly or through equality with a reference
// already pinned (seed). OR pins only what every branch pins; AND pins what any conjunct pins, to a fixpoint. A
// comparison in a projection or any other position never reaches here.
func scopeBindings(tokens []string, column string, seed map[string]bool) map[string]bool {
	for len(tokens) > 1 && tokens[0] == "(" && scopeClose(tokens, 0) == len(tokens)-1 {
		tokens = tokens[1 : len(tokens)-1]
	}
	out := maps.Clone(seed)
	if branches := scopeParts(tokens, "or"); len(branches) > 1 {
		var common map[string]bool
		for _, branch := range branches {
			bound := scopeBindings(branch, column, seed)
			if common == nil {
				common = bound
				continue
			}
			for ref := range common {
				if !bound[ref] {
					delete(common, ref)
				}
			}
		}
		return common
	}
	if conjuncts := scopeParts(tokens, "and"); len(conjuncts) > 1 {
		for grew := true; grew; {
			grew = false
			for _, conjunct := range conjuncts {
				for ref := range scopeBindings(conjunct, column, out) {
					if !out[ref] {
						out[ref], grew = true, true
					}
				}
			}
		}
		return out
	}
	if equal := scopeParts(tokens, "="); len(equal) == 2 {
		left, right := scopeColumnRef(equal[0], column), scopeColumnRef(equal[1], column)
		switch {
		case left != "" && scopeParameter(equal[1]):
			out[left] = true
		case right != "" && scopeParameter(equal[0]):
			out[right] = true
		case left != "" && right != "" && seed[left]:
			out[right] = true
		case left != "" && right != "" && seed[right]:
			out[left] = true
		}
		return out
	}
	if in := scopeParts(tokens, "in"); len(in) == 2 {
		values, ref := in[1], scopeColumnRef(in[0], column)
		if ref != "" && len(values) >= 3 && values[0] == "(" && scopeClose(values, 0) == len(values)-1 && !slices.ContainsFunc(scopeParts(values[1:len(values)-1], ","), func(value []string) bool { return !scopeParameter(value) }) {
			out[ref] = true
		}
	}
	return out
}

var scopeClauseEnd = []string{"where", "group", "order", "limit", "returning", "union", "having", "join", "left", "right", "inner", "full", "cross", "on", "set", "window", "do"}

// safety: the WHERE clause and every join's ON clause restrict rows; ON CONFLICT is a write target, not a filter.
func scopeRestrictions(tokens []string) []string {
	var out []string
	depth := 0
	for i := 0; i < len(tokens); i++ {
		switch tokens[i] {
		case "(":
			depth++
			continue
		case ")":
			depth--
			continue
		}
		if depth != 0 || (tokens[i] != "where" && tokens[i] != "on") || tokens[i] == "on" && i+1 < len(tokens) && tokens[i+1] == "conflict" {
			continue
		}
		end, inner := len(tokens), 0
		for j := i + 1; j < len(tokens); j++ {
			if tokens[j] == "(" {
				inner++
			} else if tokens[j] == ")" {
				inner--
			} else if inner == 0 && slices.Contains(scopeClauseEnd, tokens[j]) {
				end = j
				break
			}
		}
		if len(out) > 0 {
			out = append(out, "and")
		}
		out = append(out, "(")
		out = append(out, tokens[i+1:end]...)
		out = append(out, ")")
		i = end - 1
	}
	return out
}

// safety: the relations one query reads or writes at its own depth, as the reference each is named by; INTO names
// an insert target, which the insert reader judges by its column list.
func scopeRelationRefs(tokens []string, tables []string) []string {
	var refs []string
	depth, inFrom := 0, false
	for i, token := range tokens {
		switch token {
		case "(":
			depth++
		case ")":
			depth--
		}
		if depth != 0 {
			continue
		}
		introduces := slices.Contains([]string{"from", "join", "update"}, token) || token == "," && inFrom
		if token == "from" || token == "join" {
			inFrom = true
		} else if slices.Contains(scopeClauseEnd, token) || token == "select" {
			inFrom = false
		}
		if !introduces || i+1 >= len(tokens) || !slices.Contains(tables, tokens[i+1]) {
			continue
		}
		ref := tokens[i+1]
		next := i + 2
		if next < len(tokens) && tokens[next] == "as" {
			next++
		}
		if next < len(tokens) && scopeIdentRe.MatchString(tokens[next]) && !slices.Contains(scopeClauseEnd, tokens[next]) && !slices.Contains(sqlClauseWords, tokens[next]) && !slices.Contains([]string{"natural", "outer", "using"}, tokens[next]) {
			ref = tokens[next]
		}
		refs = append(refs, ref)
	}
	return refs
}

func scopeEveryRelationBound(tokens, parents []string, column string, tables []string) bool {
	if len(scopeRelationRefs(tokens, tables)) == 0 {
		// safety: a query whose relations the reader cannot name (a CTE result, a table spliced in at run time)
		// keeps the any-binding rule; the statement's tenant tables are still judged where they are named.
		return scopeFilter(tokens, parents)
	}
	return scopeRelationsBound(tokens, parents, column, tables)
}

// safety: every relation of tables in one query must be pinned on column by its restricting predicates; a bare
// column pins the query's only such relation.
func scopeRelationsBound(tokens, parents []string, column string, tables []string) bool {
	refs := scopeRelationRefs(tokens, tables)
	seed := map[string]bool{}
	for _, parent := range parents {
		seed[parent] = true
	}
	bound := scopeBindings(scopeRestrictions(tokens), column, seed)
	for _, ref := range refs {
		if !bound[ref+"."+column] && !(len(refs) == 1 && bound[column]) {
			return false
		}
	}
	return true
}

func scopeBoundRefs(tokens []string, column string, tables []string) []string {
	var out []string
	for _, ref := range scopeRelationRefs(tokens, tables) {
		out = append(out, ref+"."+column)
	}
	return out
}
