package controller

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// safety: these paths name a whole segment with a variable the parser cannot
// resolve, so the test lists the values the dashboard passes.
var frontendSegmentValues = map[string][]string{
	"action": {"pause", "resume", "run"},
}

// safety: each entry is a route the dashboard calls only where another
// process serves it, with where that is.
var frontendRoutesServedElsewhere = map[string]string{
	"GET /api/v1/queue": "the laptop admission queue, which `sparkwing serve` answers; the nav shows the tab only there",
}

// safety: an empty method is a call site that names none, such as a path built
// from another literal's constant, and any registered method satisfies it.
type frontendCall struct {
	method string
	path   string
	site   string
}

func TestEveryFrontendAPICallIsAControllerRoute(t *testing.T) {
	calls := frontendAPICalls(t, filepath.Join("..", "..", "web", "src", "lib"))
	if len(calls) < 80 {
		t.Fatalf("found %d frontend API calls; the parser has lost track of the call sites", len(calls))
	}
	authed, public := New(nil, nil).routers(nil)
	for _, c := range calls {
		key := c.method + " " + c.path
		if _, elsewhere := frontendRoutesServedElsewhere[strings.TrimSpace(key)]; elsewhere {
			continue
		}
		if !muxServes(authed, public, c.method, c.path) {
			t.Errorf("%s: the dashboard calls %s and the controller registers no such route", c.site, strings.TrimSpace(key))
		}
	}
}

func muxServes(authed, public *http.ServeMux, method, path string) bool {
	methods := []string{method}
	if method == "" {
		methods = []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}
	}
	for _, m := range methods {
		req := httptest.NewRequest(m, path, nil)
		for _, mux := range []*http.ServeMux{public, authed} {
			if _, pattern := mux.Handler(req); pattern != "" && pattern != "/" {
				return true
			}
		}
	}
	return false
}

var (
	sendMethodRE  = regexp.MustCompile(`send\(\s*"([A-Z]+)",\s*$`)
	fieldMethodRE = regexp.MustCompile(`method:\s*"([A-Z]+)"`)
	boundaryRE    = regexp.MustCompile(`\n(?:export |async function |function )`)
	constDeclRE   = regexp.MustCompile(`const (\w+) = $`)
)

func frontendAPICalls(t *testing.T, dir string) []frontendCall {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.ts"))
	if err != nil {
		t.Fatal(err)
	}
	var calls []frontendCall
	for _, file := range files {
		if strings.HasSuffix(file, ".test.ts") {
			continue
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		calls = append(calls, callsIn(t, filepath.Base(file), string(raw))...)
	}
	return calls
}

type tsLiteral struct {
	start, end int
	parts      []tsPart
}

type tsPart struct {
	text string
	expr bool
}

func callsIn(t *testing.T, name, src string) []frontendCall {
	t.Helper()
	consts := map[string]string{}
	var calls []frontendCall
	for _, lit := range tsLiterals(src) {
		path, composite, ok := apiPath(lit.parts, consts)
		if !ok {
			continue
		}
		if m := constDeclRE.FindStringSubmatch(src[max(0, lit.start-64):lit.start]); m != nil {
			consts[m[1]] = path
		}
		method := callMethod(src, lit)
		if method == "" && !composite {
			method = http.MethodGet
		}
		site := name + ":" + strconv.Itoa(strings.Count(src[:lit.start], "\n")+1)
		for _, p := range expandSegments(path) {
			calls = append(calls, frontendCall{method: method, path: p, site: site})
		}
	}
	return calls
}

// safety: the method a call names sits either just before its path, as
// send("POST", path, ...), or in the options that follow it before the next
// function begins; a path with neither is a GET.
func callMethod(src string, lit tsLiteral) string {
	if m := sendMethodRE.FindStringSubmatch(src[max(0, lit.start-64):lit.start]); m != nil {
		return m[1]
	}
	rest := src[lit.end:]
	if loc := boundaryRE.FindStringIndex(rest); loc != nil {
		rest = rest[:loc[0]]
	}
	if m := fieldMethodRE.FindStringSubmatch(rest); m != nil {
		return m[1]
	}
	return ""
}

// safety: a variable segment renders as x so the mux matches it the way it matches an id; a variable that is not a
// whole segment, such as a query string, ends the path.
func apiPath(parts []tsPart, consts map[string]string) (string, bool, bool) {
	var b strings.Builder
	composite := false
	for i, p := range parts {
		if !p.expr {
			text := p.text
			if q := strings.IndexByte(text, '?'); q >= 0 {
				b.WriteString(text[:q])
				break
			}
			b.WriteString(text)
			continue
		}
		expr := strings.TrimSpace(p.text)
		if expr == "API_URL" && b.Len() == 0 {
			continue
		}
		if base, ok := consts[expr]; ok && b.Len() == 0 {
			b.WriteString(base)
			composite = true
			continue
		}
		atSegmentStart := strings.HasSuffix(b.String(), "/")
		nextStartsSegment := i+1 == len(parts) || (!parts[i+1].expr && (parts[i+1].text == "" ||
			parts[i+1].text[0] == '/' || parts[i+1].text[0] == '?'))
		if !atSegmentStart {
			break
		}
		if _, ok := frontendSegmentValues[expr]; ok {
			b.WriteString("{" + expr + "}")
		} else {
			b.WriteString("x")
		}
		if !nextStartsSegment {
			break
		}
	}
	path := b.String()
	return path, composite, strings.HasPrefix(path, "/api/v1/") || path == "/api/v1"
}

func expandSegments(path string) []string {
	out := []string{path}
	for name, values := range frontendSegmentValues {
		token := "{" + name + "}"
		var next []string
		for _, p := range out {
			if !strings.Contains(p, token) {
				next = append(next, p)
				continue
			}
			for _, v := range values {
				next = append(next, strings.Replace(p, token, v, 1))
			}
		}
		out = next
	}
	return slices.Compact(out)
}

func tsLiterals(src string) []tsLiteral {
	var out []tsLiteral
	for i := 0; i < len(src); i++ {
		switch c := src[i]; {
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return out
			}
			i += end + 3
		case c == '"' || c == '\'':
			j := i + 1
			for j < len(src) && src[j] != c && src[j] != '\n' {
				if src[j] == '\\' {
					j++
				}
				j++
			}
			out = append(out, tsLiteral{start: i, end: j + 1, parts: []tsPart{{text: src[i+1 : min(j, len(src))]}}})
			i = j
		case c == '`':
			lit, end := templateLiteral(src, i)
			out = append(out, lit)
			i = end - 1
		}
	}
	return out
}

func templateLiteral(src string, start int) (tsLiteral, int) {
	lit := tsLiteral{start: start}
	var text strings.Builder
	i := start + 1
	for i < len(src) && src[i] != '`' {
		if src[i] == '\\' && i+1 < len(src) {
			text.WriteByte(src[i+1])
			i += 2
			continue
		}
		if src[i] == '$' && i+1 < len(src) && src[i+1] == '{' {
			lit.parts = append(lit.parts, tsPart{text: text.String()})
			text.Reset()
			depth, j := 1, i+2
			for j < len(src) && depth > 0 {
				switch src[j] {
				case '{':
					depth++
				case '}':
					depth--
				case '`':
					_, end := templateLiteral(src, j)
					j = end - 1
				}
				j++
			}
			lit.parts = append(lit.parts, tsPart{text: src[i+2 : j-1], expr: true})
			i = j
			continue
		}
		text.WriteByte(src[i])
		i++
	}
	lit.parts = append(lit.parts, tsPart{text: text.String()})
	lit.end = i + 1
	return lit, i + 1
}
