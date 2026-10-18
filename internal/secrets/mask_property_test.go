package secrets

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

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

// Invariant: no registered secret is visible in masked text, raw or after
// decoding JSON escapes, however its characters are escaped or overlap others.
func TestMaskLeavesNoSecretVisibleProperty(t *testing.T) {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
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
			streamed.WriteString(pieces.Mask(rest[:n], n == len(rest)))
			rest = rest[n:]
		}
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

// jsonDecodedView decodes each escape sequence in s with encoding/json and
// keeps every other byte, so the oracle shares no decoding with the masker.
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

func TestCachedRegistersBeforeAnotherReaderSeesTheValue(t *testing.T) {
	masker := NewMasker()
	read := make(chan struct{})
	c := NewCached(SourceFunc(func(name string) (string, bool, error) {
		close(read)
		return "secret-value", true, nil
	}), masker)
	masker.mu.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = c.Resolve(context.Background(), "TOKEN")
	}()
	<-read
	for range 10000 {
		c.mu.RLock()
		_, cached := c.cache["TOKEN"]
		c.mu.RUnlock()
		if cached {
			masker.mu.Unlock()
			<-done
			t.Fatal("value served from the cache before the masker registered it")
		}
		runtime.Gosched()
	}
	masker.mu.Unlock()
	<-done
	if got := masker.Mask("secret-value"); got != "***" {
		t.Fatalf("Mask = %q", got)
	}
}
