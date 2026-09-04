package main

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/sparkwing-dev/sparkwing/internal/apiroutes"
)

const scopeKey = "x-sparkwing-scope"

const header = `GENERATED IN PART by internal/apispec. The path and method inventory and
every ` + scopeKey + ` value are read from the route registrations in
pkg/controller/server.go; summaries, schemas, and examples are hand-written
here. Rewrite this file with ` + "`bash bin/gen-api-docs.sh`" + ` and prove it current
with ` + "`bash bin/check-api-spec.sh`" + `. Give a route's authorization as
` + scopeKey + `, never as prose, so no two sentences can disagree.

Every object shape names the Go type it mirrors as ` + goTypeKey + `, and the
check holds its members to that type's JSON tags: a renamed, dropped, or
retyped field fails rather than leaving the document quietly untrue. Use
` + goTypeKey + `: none only where the controller writes the shape without a
struct behind it.`

const stubSummary = "Registered route awaiting a description."

const stubDescription = "Seeded by internal/apispec from the route table. " +
	"Replace this text with what the route does, what it accepts, and what it answers."

var httpMethods = []string{"get", "put", "post", "delete", "patch", "head", "options", "trace"}

var wildcardRE = regexp.MustCompile(`\{(\w+)\.\.\.\}`)

var pathParamRE = regexp.MustCompile(`\{(\w+)\}`)

func rewrite(spec string, routes []apiroutes.Route, scopes []string) (string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(stripHeader(spec)), &doc); err != nil {
		return "", fmt.Errorf("parse: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return "", fmt.Errorf("want a single YAML mapping document")
	}
	root := doc.Content[0]

	paths := mapValue(root, "paths")
	if paths == nil || paths.Kind != yaml.MappingNode {
		return "", fmt.Errorf("no paths mapping")
	}

	registered := map[string]apiroutes.Route{}
	for _, r := range routes {
		registered[r.Method+" "+specPath(r.Path)] = r
	}

	documented, err := stampDocumented(paths, registered, scopes)
	if err != nil {
		return "", err
	}
	seedMissing(paths, routes, documented)

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return "", fmt.Errorf("encode: %w", err)
	}
	if err := enc.Close(); err != nil {
		return "", fmt.Errorf("encode: %w", err)
	}
	return comment(header) + "\n" + space(buf.String()), nil
}

func stripHeader(spec string) string {
	lines := strings.Split(spec, "\n")
	i := 0
	for i < len(lines) && (strings.HasPrefix(lines[i], "#") || strings.TrimSpace(lines[i]) == "") {
		i++
	}
	return strings.Join(lines[i:], "\n")
}

func comment(text string) string {
	var b strings.Builder
	for _, line := range strings.Split(text, "\n") {
		b.WriteString(strings.TrimRight("# "+line, " ") + "\n")
	}
	return b.String()
}

func stampDocumented(paths *yaml.Node, registered map[string]apiroutes.Route, scopes []string) (map[string]bool, error) {
	documented := map[string]bool{}
	var phantom, prose, malformed []string
	for i := 0; i+1 < len(paths.Content); i += 2 {
		path := paths.Content[i].Value
		item := paths.Content[i+1]
		if item.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j+1 < len(item.Content); j += 2 {
			key := item.Content[j].Value
			if key != "summary" && key != "description" {
				continue
			}
			if scope := namedScope(item.Content[j+1].Value, scopes); scope != "" {
				prose = append(prose, path+": "+key+" names "+scope)
			}
		}
		for j := 0; j+1 < len(item.Content); j += 2 {
			method := strings.ToLower(item.Content[j].Value)
			if !isMethod(method) {
				continue
			}
			opKey := strings.ToUpper(method) + " " + path
			route, ok := registered[opKey]
			if !ok {
				phantom = append(phantom, opKey)
				continue
			}
			documented[opKey] = true
			op := item.Content[j+1]
			setScalar(op, scopeKey, strings.ReplaceAll(route.Scope, "`", ""), true)
			if claim := scopeProse(op, scopes); claim != "" {
				prose = append(prose, opKey+": "+claim)
			}
			malformed = append(malformed, badResponses(opKey, op)...)
		}
	}
	if len(phantom) > 0 {
		sort.Strings(phantom)
		return nil, fmt.Errorf("documents %d route(s) the controller does not register; delete them or register them:\n  %s",
			len(phantom), strings.Join(phantom, "\n  "))
	}
	if len(prose) > 0 {
		sort.Strings(prose)
		return nil, fmt.Errorf("%d operation(s) state a scope in prose, which drifts from the route table; drop the phrase and let %s carry it:\n  %s",
			len(prose), scopeKey, strings.Join(prose, "\n  "))
	}
	if len(malformed) > 0 {
		sort.Strings(malformed)
		return nil, fmt.Errorf("%d response object(s) carry a field OpenAPI 3.0 does not allow, usually an unquoted comma splitting a description:\n  %s",
			len(malformed), strings.Join(malformed, "\n  "))
	}
	return documented, nil
}

// safety: any other member on a Response Object, x- extensions aside,
// means the document does not say what its author wrote.
var responseFields = map[string]bool{
	"description": true,
	"headers":     true,
	"content":     true,
	"links":       true,
	"$ref":        true,
}

func badResponses(opKey string, op *yaml.Node) []string {
	responses := mapValue(op, "responses")
	if responses == nil || responses.Kind != yaml.MappingNode {
		return nil
	}
	var bad []string
	for i := 0; i+1 < len(responses.Content); i += 2 {
		status, response := responses.Content[i].Value, responses.Content[i+1]
		if response.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j+1 < len(response.Content); j += 2 {
			field := response.Content[j].Value
			if responseFields[field] || strings.HasPrefix(field, "x-") {
				continue
			}
			bad = append(bad, opKey+" "+status+": unexpected field "+strconv.Quote(field))
		}
	}
	return bad
}

func seedMissing(paths *yaml.Node, routes []apiroutes.Route, documented map[string]bool) {
	for _, r := range routes {
		path := specPath(r.Path)
		if documented[r.Method+" "+path] {
			continue
		}
		item := mapValue(paths, path)
		if item == nil {
			item = &yaml.Node{Kind: yaml.MappingNode}
			if params := pathParameters(path); params != nil {
				appendPair(item, "parameters", params)
			}
			appendPair(paths, path, item)
		}
		appendPair(item, strings.ToLower(r.Method), stub(r.Scope))
	}
}

func stub(scope string) *yaml.Node {
	op := &yaml.Node{Kind: yaml.MappingNode}
	appendPair(op, scopeKey, scalar(scope))
	appendPair(op, "summary", scalar(stubSummary))
	appendPair(op, "description", scalar(stubDescription))
	fallback := &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle}
	appendPair(fallback, "description", scalar("Undescribed."))
	responses := &yaml.Node{Kind: yaml.MappingNode}
	appendPair(responses, "default", fallback)
	appendPair(op, "responses", responses)
	return op
}

func pathParameters(path string) *yaml.Node {
	names := pathParamRE.FindAllStringSubmatch(path, -1)
	if len(names) == 0 {
		return nil
	}
	seq := &yaml.Node{Kind: yaml.SequenceNode}
	for _, m := range names {
		param := &yaml.Node{Kind: yaml.MappingNode}
		appendPair(param, "in", scalar("path"))
		appendPair(param, "name", scalar(m[1]))
		appendPair(param, "required", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"})
		schema := &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle}
		appendPair(schema, "type", scalar("string"))
		appendPair(param, "schema", schema)
		seq.Content = append(seq.Content, param)
	}
	return seq
}

func scopeProse(node *yaml.Node, scopes []string) string {
	if node.Kind == yaml.SequenceNode {
		for _, child := range node.Content {
			if claim := scopeProse(child, scopes); claim != "" {
				return claim
			}
		}
		return ""
	}
	if node.Kind != yaml.MappingNode {
		return ""
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := node.Content[i].Value, node.Content[i+1]
		if key == "schema" || key == "schemas" {
			continue
		}
		if (key == "summary" || key == "description") && value.Kind == yaml.ScalarNode {
			if scope := namedScope(value.Value, scopes); scope != "" {
				return key + " names " + scope
			}
			continue
		}
		if claim := scopeProse(value, scopes); claim != "" {
			return claim
		}
	}
	return ""
}

// safety: these sentences name a scope to say which fields it fills, not
// which scope the route demands, so namedScope reads past them.
var proseNarrowing = []string{
	"honored for an `admin` token and for a `nodes.claim` token holding an unexpired claim",
	"Admits `runs.read`, but fills `env_json` only for an `admin` principal",
}

var proseMarkup = strings.NewReplacer("`", "", "*", "", "_", "")

func namedScope(text string, scopes []string) string {
	text = strings.Join(strings.Fields(text), " ")
	for _, allowed := range proseNarrowing {
		text = strings.ReplaceAll(text, allowed, "")
	}
	text = proseMarkup.Replace(strings.ToLower(text))
	for _, scope := range scopes {
		for _, spelling := range []string{scope, strings.ReplaceAll(scope, ".", "-")} {
			if scopeTokenRE(spelling).MatchString(text) {
				return spelling
			}
		}
	}
	return ""
}

var scopeTokenREs sync.Map

// safety: a bare scope token anywhere in the prose is a second authorization
// statement, so the boundary stops only at letters and digits.
func scopeTokenRE(spelling string) *regexp.Regexp {
	if re, ok := scopeTokenREs.Load(spelling); ok {
		return re.(*regexp.Regexp)
	}
	re := regexp.MustCompile(`(^|[^a-z0-9])` + regexp.QuoteMeta(spelling) + `([^a-z0-9]|$)`)
	scopeTokenREs.Store(spelling, re)
	return re
}

func specPath(routePath string) string {
	return wildcardRE.ReplaceAllString(routePath, "{$1}")
}

func isMethod(s string) bool {
	for _, m := range httpMethods {
		if s == m {
			return true
		}
	}
	return false
}

func scalar(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Value: v}
}

func mapValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func appendPair(m *yaml.Node, key string, value *yaml.Node) {
	m.Content = append(m.Content, scalar(key), value)
}

func setScalar(m *yaml.Node, key, value string, front bool) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = scalar(value)
			return
		}
	}
	pair := []*yaml.Node{scalar(key), scalar(value)}
	if front {
		m.Content = append(pair, m.Content...)
		return
	}
	m.Content = append(m.Content, pair...)
}

func scopeValues(scopes map[string]string) []string {
	seen := map[string]bool{}
	var vals []string
	for _, v := range scopes {
		if !seen[v] {
			seen[v] = true
			vals = append(vals, v)
		}
	}
	sort.Strings(vals)
	return vals
}

func space(doc string) string {
	lines := strings.Split(doc, "\n")
	var out []string
	for i, line := range lines {
		if i > 0 && (topLevelKey(line) || pathKey(line)) {
			out = append(out, "")
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func topLevelKey(line string) bool {
	return len(line) > 0 && line[0] != ' ' && line[0] != '#' && strings.Contains(line, ":")
}

func pathKey(line string) bool {
	return strings.HasPrefix(line, "  /") && strings.HasSuffix(line, ":")
}
