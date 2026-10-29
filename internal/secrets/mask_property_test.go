package secrets

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"testing"
)

func newTestMasker(values ...string) *Masker {
	m := NewMasker()
	for _, value := range values {
		if err := m.Register(value); err != nil {
			panic(err)
		}
	}
	return m
}

func TestMaskMergesOverlappingSecrets(t *testing.T) {
	m := newTestMasker("abcXYZ", "XYZdef")
	if got := m.Mask("abcXYZdef"); got != "***" {
		t.Fatalf("Mask = %q, want ***", got)
	}
	if got := m.Mask("XYZdef abcXYZ"); got != "*** ***" {
		t.Fatalf("Mask = %q", got)
	}
}

func TestMaskHidesEveryJSONEscapeSpelling(t *testing.T) {
	m := newTestMasker(`p@ss w/rd+"x"é\k`)
	for _, line := range []string{
		`p@ss w\/rd+\"x\"é\\k`,
		`p@ss w/rd+\u0022x\u0022\u00e9\u005ck`,
		`\u0070@ss w\/rd\u002b"x"\u00E9\\k`,
		`p@ss w/rd+\"x\"\u00e9\\k`,
	} {
		if got := m.Mask("msg=" + line + " end"); got != "msg=*** end" {
			t.Errorf("Mask(%q) = %q", line, got)
		}
	}
}

// Invariant: no registered secret free of '*' is visible in masked text, raw or
// after decoding JSON escapes, however its characters are escaped or overlap
// others. A '*' beside a replacement marker is the accepted residual MASKING.md
// describes, so the alphabet leaves it out.
func TestMaskLeavesNoSecretVisibleProperty(t *testing.T) {
	r := rand.New(rand.NewSource(817))
	alpha := []rune(`ab/"\é`)
	word := func(n int) string {
		var b strings.Builder
		for range n {
			b.WriteRune(alpha[r.Intn(len(alpha))])
		}
		return b.String()
	}
	for iter := range 20000 {
		var secrets []string
		for range 1 + r.Intn(3) {
			secrets = append(secrets, word(4+r.Intn(5)))
		}
		m := newTestMasker(secrets...)
		var text strings.Builder
		for range 1 + r.Intn(4) {
			text.WriteString(word(r.Intn(6)))
			text.WriteString(escapeRandomly(r, secrets[r.Intn(len(secrets))]))
		}
		in := text.String()
		got := m.Mask(in)
		assertNoSecretVisible(t, iter, secrets, in, got)
		pieces := m.Stream()
		var streamed strings.Builder
		rest := in
		for len(rest) > 0 {
			n := 1 + r.Intn(len(rest))
			streamed.WriteString(pieces.Mask(rest[:n]))
			rest = rest[n:]
		}
		streamed.WriteString(pieces.Flush())
		assertNoSecretVisible(t, iter, secrets, in, streamed.String())
	}
}

func assertNoSecretVisible(t *testing.T, iter int, secrets []string, in, got string) {
	t.Helper()
	decoded := jsonDecodedView(got)
	for _, s := range secrets {
		if strings.Contains(got, s) || strings.Contains(decoded, s) {
			t.Fatalf("iter %d: secrets %q in %q masked to %q", iter, secrets, in, got)
		}
	}
}

// safety: the oracle decodes escapes with encoding/json, never the masker's own decoder.
func jsonDecodedView(s string) string {
	escape := regexp.MustCompile(`\\(?:u[dD][89abAB][0-9a-fA-F]{2}\\u[dD][c-fC-F][0-9a-fA-F]{2}|u[0-9a-fA-F]{4}|["\\/bfnrt])`)
	return escape.ReplaceAllStringFunc(s, func(sequence string) string {
		var decoded string
		if json.Unmarshal([]byte(`"`+sequence+`"`), &decoded) != nil {
			return sequence
		}
		return decoded
	})
}

func escapeRandomly(r *rand.Rand, s string) string {
	var b strings.Builder
	for _, c := range s {
		switch {
		case c == '"' || c == '\\':
			b.WriteByte('\\')
			b.WriteRune(c)
		case c == '/' && r.Intn(2) == 0:
			b.WriteString(`\/`)
		case r.Intn(3) == 0:
			fmt.Fprintf(&b, `\u%04x`, c)
		default:
			b.WriteRune(c)
		}
	}
	return b.String()
}

func TestRegisterOfAKnownValueDoesNoWork(t *testing.T) {
	m := NewMasker()
	for i := range 20000 {
		m.Register(fmt.Sprintf("swt_%032x", i))
	}
	allocs := testing.AllocsPerRun(50, func() { m.Register("swt_00000000000000000000000000000001") })
	if allocs > 0 {
		t.Fatalf("re-registering a known value allocated %v times per call", allocs)
	}
}

// Concatenated emissions, including a final EOF flush, must never expose any
// registered spelling even when matching or replacement crosses a chunk edge.
// Decision 49 accepts marker-containing secrets and values containing '*' that
// replacement may form beside a marker across emissions that cannot be recalled.
func TestStreamReplacementClosureProperty(t *testing.T) {
	random := rand.New(rand.NewSource(43109))
	candidates := []string{
		"abcd", "bcde", "abcdxyz", "xyzé", "line\none", `slash/quote"`,
		"private-token", "two private words", "x%2Fy", "éééé",
		"*xyz", "ab**", "**xy", "x**y",
	}
	for iteration := range 1200 {
		m := NewMasker()
		count := 1 + random.Intn(4)
		for range count {
			if err := m.Register(candidates[random.Intn(len(candidates))]); err != nil {
				t.Fatal(err)
			}
		}
		var input strings.Builder
		for range 1 + random.Intn(6) {
			input.WriteString([]string{"a", "b", "*", "\n", `\\u0061`, "%2f", "\"", "public"}[random.Intn(8)])
			input.WriteString(candidates[random.Intn(len(candidates))])
		}
		data := input.String()
		stream := m.Stream()
		var result strings.Builder
		for len(data) > 0 {
			n := 1 + random.Intn(min(len(data), 9))
			result.WriteString(stream.Mask(data[:n]))
			data = data[n:]
			if random.Intn(23) == 0 {
				if err := m.Register("late-secret"); err != nil {
					t.Fatal(err)
				}
				result.WriteString(stream.Mask("late-secret"))
			}
		}
		result.WriteString(stream.Flush())
		rendered := result.String()
		decoded := jsonDecodedView(rendered)
		for _, spelling := range m.Values() {
			if strings.Contains(spelling, "*") {
				continue // Decision 49 accepts values formed with '*' adjacent to a replacement marker.
			}
			if strings.Contains(rendered, spelling) || strings.Contains(decoded, spelling) {
				t.Fatalf("iteration %d: spelling %q survived in %q", iteration, spelling, rendered)
			}
		}
	}
}

func FuzzStreamReplacementClosure(f *testing.F) {
	for _, seed := range []string{"aaaa", "ababxxxx", "ab**", `quoted\u0022value`, "lower%2fvalue", "***"} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 || len(data) > 1024 {
			t.Skip()
		}
		value := "private-" + strings.ToValidUTF8(string(data), "?")
		// Decision 49 also accepts values formed with '*' adjacent to a replacement marker.
		if strings.Contains(value, "*") {
			t.Skip()
		}
		m := newTestMasker(value, "ab**", "xxxx", "aaaa", "*xyz")
		stream := m.Stream()
		input := string(data) + value + escapeEveryRune(value) + strings.Repeat("ab", int(data[0])) + "xxxxxyz"
		var output strings.Builder
		at := 0
		for len(input) > 0 {
			n := min(1+int(data[at%len(data)])%31, len(input))
			output.WriteString(stream.Mask(input[:n]))
			input = input[n:]
			at++
		}
		if err := m.Register("issued-private-value"); err != nil {
			t.Fatal(err)
		}
		output.WriteString(stream.Mask("issued-private-value"))
		output.WriteString(stream.Flush())
		view := output.String()
		for range 3 {
			for _, pattern := range m.Values() {
				if strings.Contains(pattern, "*") {
					continue // Decision 49 accepts this marker-edge residual.
				}
				if strings.Contains(view, pattern) {
					t.Fatalf("registered spelling %q survived in %q", pattern, output.String())
				}
			}
			view = jsonDecodedView(view)
		}
	})
}
