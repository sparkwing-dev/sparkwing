package secrets

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

func TestStreamMasksAcrossNewlinesAndChunkEdges(t *testing.T) {
	m := NewMasker()
	if err := m.Register("line-one\nline-two"); err != nil {
		t.Fatal(err)
	}
	input := "before line-one\nline-two after"
	for cut := range len(input) + 1 {
		stream := m.Stream()
		got := stream.Mask(input[:cut]) + stream.Mask(input[cut:]) + stream.Flush()
		if got != "before *** after" {
			t.Fatalf("cut %d: %q", cut, got)
		}
	}
}

func TestStreamEmitsLargeInputsWithoutRetainingEntireStream(t *testing.T) {
	m := NewMasker()
	if err := m.Register("hunter2-very-secret"); err != nil {
		t.Fatal(err)
	}
	stream := m.Stream()
	input := strings.Repeat("x", 1<<20)
	first := stream.Mask(input)
	if len(first) < len(input)-2*36*longestPattern(m.Values())-64 {
		t.Fatalf("first output only %d bytes", len(first))
	}
	if got := first + stream.Flush(); got != input {
		t.Fatalf("stream lost bytes: %d", len(got))
	}
}

func TestStreamHidesReplacementJoinsAndRegistryUpdates(t *testing.T) {
	m := newTestMasker("abcd", "xyzz")
	stream := m.Stream()
	var got strings.Builder
	got.WriteString(stream.Mask("prefix ab"))
	got.WriteString(stream.Mask("cd xy"))
	if err := m.Register("new-secret"); err != nil {
		t.Fatal(err)
	}
	got.WriteString(stream.Mask("zz new-secret suffix"))
	got.WriteString(stream.Flush())
	if strings.Contains(got.String(), "abcd") || strings.Contains(got.String(), "xyzz") || strings.Contains(got.String(), "new-secret") {
		t.Fatalf("registered value survived: %q", got.String())
	}
}

func TestStreamClosesMatchesCreatedAtReplacementMarkerEdges(t *testing.T) {
	m := newTestMasker("abcd", "*xyz", "yz**", "**xy")
	prefix := strings.Repeat(".", 12000)
	input := "abcdxyz yzabcd abcdxy"
	for cut := range len(input) + 1 {
		stream := m.Stream()
		got := stream.Mask(prefix+input[:cut]) + stream.Mask(input[cut:]) + stream.Flush()
		for _, spelling := range m.Values() {
			if strings.Contains(got, spelling) {
				t.Fatalf("cut %d recreated spelling %q", cut, spelling)
			}
		}
		if got != prefix+m.Mask(input) {
			t.Fatalf("cut %d differs from whole-text masking", cut)
		}
	}
}

func TestNilMaskerStreamPassesTextThrough(t *testing.T) {
	var m *Masker
	s := m.Stream()
	if got := s.Mask("abc") + s.Mask("def") + s.Flush(); got != "abcdef" {
		t.Fatalf("got %q", got)
	}
}

var streamBenchmarkSink string

func BenchmarkStreamMask(b *testing.B) {
	m := NewMasker()
	for _, value := range []string{"hunter2-very-secret", "private/token+one", "other-private-value"} {
		if err := m.Register(value); err != nil {
			b.Fatal(err)
		}
	}
	payload := strings.Repeat("safe log text with ordinary data\n", 1024) + "hunter2-very-secret\n"
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	for range b.N {
		stream := m.Stream()
		streamBenchmarkSink = stream.Mask(payload) + stream.Flush()
	}
}

func TestStreamMasksEveryPercentEscapeCaseAcrossChunks(t *testing.T) {
	m := NewMasker()
	if err := m.Register("my/é-token"); err != nil {
		t.Fatal(err)
	}
	spellings := []string{
		"my%2F%C3%A9-token", "my%2f%c3%a9-token", "my%2F%c3%A9-token",
		"my%2f%C3%a9-token", "my%2F%C3%a9-token",
	}
	for _, spelling := range spellings {
		for cut := range len(spelling) + 1 {
			stream := m.Stream()
			got := stream.Mask(spelling[:cut]) + stream.Mask(spelling[cut:]) + stream.Flush()
			if got != "***" {
				t.Fatalf("%q at %d: %q", spelling, cut, got)
			}
		}
	}
}

func TestPercentEscapesMatchIndependentlyOfNeighboringPercentSigns(t *testing.T) {
	m := newTestMasker("ab/f-token", "AB/f-token")
	for _, input := range []string{"%ab%2ff-token!", "%AB%2ff-token!", "%aab%2Ff-token!"} {
		want := input[:len(input)-len("ab%2ff-token!")] + "***!"
		if got := m.Mask(input); got != want {
			t.Fatalf("Mask(%q) = %q, want %q", input, got, want)
		}
		for cut := range len(input) + 1 {
			stream := m.Stream()
			if got := stream.Mask(input[:cut]) + stream.Mask(input[cut:]) + stream.Flush(); got != want {
				t.Fatalf("cut %d: %q", cut, got)
			}
		}
	}
	if got := newTestMasker("ab/f-token").Mask("%AB%2ff-token!"); got != "%AB%2ff-token!" {
		t.Fatalf("ordinary letters lost case sensitivity: %q", got)
	}
}

func BenchmarkStreamMaskLargeRegistry(b *testing.B) {
	m := NewMasker()
	for i := range 64 {
		if err := m.Register(fmt.Sprintf("private-value-%04d", i)); err != nil {
			b.Fatal(err)
		}
	}
	payload := strings.Repeat("ordinary output line\n", 512) + "private-value-0042"
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	for range b.N {
		stream := m.Stream()
		streamBenchmarkSink = stream.Mask(payload) + stream.Flush()
	}
}

func BenchmarkStreamMaskLongSecret(b *testing.B) {
	m := NewMasker()
	secret := strings.Repeat("long-secret-", 256)
	if err := m.Register(secret); err != nil {
		b.Fatal(err)
	}
	payload := strings.Repeat("ordinary output line\n", 512) + secret
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	for range b.N {
		stream := m.Stream()
		streamBenchmarkSink = stream.Mask(payload) + stream.Flush()
	}
}

func BenchmarkStreamFirstOutput(b *testing.B) {
	m := NewMasker()
	if err := m.Register("private-value"); err != nil {
		b.Fatal(err)
	}
	payload := strings.Repeat("ordinary output line\n", 256)
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	for range b.N {
		stream := m.Stream()
		streamBenchmarkSink = stream.Mask(payload)
		if len(streamBenchmarkSink) == 0 {
			b.Fatal("first chunk produced no output")
		}
	}
}

func TestStreamMasksLongOutputBeforeEOF(t *testing.T) {
	m := NewMasker()
	secret := "long-output-secret"
	if err := m.Register(secret); err != nil {
		t.Fatal(err)
	}
	stream := m.Stream()
	prefix := strings.Repeat("safe output\n", 1<<15)
	first := stream.Mask(prefix + secret[:7])
	if len(first) < len(prefix)-2*36*longestPattern(m.Values())-64 {
		t.Fatalf("stream held %d bytes before EOF", len(prefix)-len(first))
	}
	second := stream.Mask(secret[7:] + "\nend")
	got := first + second + stream.Flush()
	if strings.Contains(got, secret) || !strings.Contains(got, "***\nend") {
		t.Fatalf("secret crossed output boundary: %q", got[len(got)-64:])
	}
}

func unicodeEscapeEveryByte(value string) string {
	var result strings.Builder
	for i := 0; i < len(value); i++ {
		fmt.Fprintf(&result, `\u%04x`, value[i])
	}
	return result.String()
}

func TestStreamMasksTwiceEscapedUnicodeAcrossEmission(t *testing.T) {
	m := NewMasker()
	secret := strings.Repeat("abcd", 50)
	if err := m.Register(secret); err != nil {
		t.Fatal(err)
	}
	spelling := unicodeEscapeEveryByte(unicodeEscapeEveryByte(secret))
	if got := m.Mask(spelling); got != "***" {
		t.Fatalf("whole spelling = %q", got)
	}
	prefix := strings.Repeat("public\n", 1<<12)
	for _, cut := range []int{1, 3217, len(spelling) / 2, len(spelling) - 1} {
		stream := m.Stream()
		first := stream.Mask(prefix + spelling[:cut])
		rest := stream.Mask(spelling[cut:]) + stream.Flush()
		if first+rest != prefix+"***" {
			t.Fatalf("cut %d emitted a partial twice-escaped spelling near %q", cut, (first + rest)[len(first+rest)-min(160, len(first+rest)):])
		}
	}
}

func escapeEveryRune(value string) string {
	var result strings.Builder
	for _, r := range value {
		fmt.Fprintf(&result, `\u%04x`, r)
	}
	return result.String()
}

func TestStreamDerivedSpellingsEscapeDepthProperty(t *testing.T) {
	random := rand.New(rand.NewSource(43110))
	raw := []string{"private-token", "my/é-token", strings.Repeat("abcd", 20), `quote"slash/`, "lines\nmore", "élan-secret"}
	prefix := strings.Repeat(".", 12000)
	for iteration := range 240 {
		m := NewMasker()
		if err := m.Register(raw[random.Intn(len(raw))]); err != nil {
			t.Fatal(err)
		}
		values := m.Values()
		value := values[random.Intn(len(values))]
		spelling := value
		depth := random.Intn(3)
		for range depth {
			spelling = escapeEveryRune(spelling)
		}
		input := prefix + spelling + "!"
		stream := m.Stream()
		var output strings.Builder
		for at := 0; at < len(input); {
			n := 1 + random.Intn(min(len(input)-at, 1024))
			output.WriteString(stream.Mask(input[at : at+n]))
			at += n
		}
		output.WriteString(stream.Flush())
		got := output.String()
		if want := m.Mask(input); got != want {
			t.Fatalf("iteration %d depth %d value %q: stream tail %q, whole tail %q", iteration, depth, value, got[max(0, len(got)-120):], want[max(0, len(want)-120):])
		}
		if m.Mask(got) != got {
			t.Fatalf("iteration %d: output not closed under masking", iteration)
		}
		view := got
		for escapeDepth := range 3 {
			for _, registered := range values {
				if strings.Contains(view, registered) {
					t.Fatalf("iteration %d: %q visible after %d escape decodes", iteration, registered, escapeDepth)
				}
			}
			view = jsonDecodedView(view)
		}
	}
}

func TestStreamRegistryUpdateBeforeValueRelease(t *testing.T) {
	m := NewMasker()
	if err := m.Register("initial-secret"); err != nil {
		t.Fatal(err)
	}
	stream := m.Stream()
	prefix := strings.Repeat(".", 10000)
	got := stream.Mask(prefix)
	late := "issued-secret"
	if err := m.Register(late); err != nil {
		t.Fatal(err)
	}
	spelling := escapeEveryRune(escapeEveryRune(late))
	for i := 0; i < len(spelling); i += 17 {
		got += stream.Mask(spelling[i:min(i+17, len(spelling))])
	}
	got += stream.Flush()
	if got != prefix+"***" {
		t.Fatalf("new registration escaped: %q", got[len(got)-min(120, len(got)):])
	}
}

func TestStreamEveryDerivedSpellingAtEachEscapeDepth(t *testing.T) {
	for _, raw := range []string{"private-token", "my/é-token", `quote"slash/`, "lines\nmore", strings.Repeat("abcd", 20)} {
		m := NewMasker()
		if err := m.Register(raw); err != nil {
			t.Fatal(err)
		}
		for _, value := range m.Values() {
			spelling := value
			for depth := range 3 {
				if depth > 0 {
					spelling = escapeEveryRune(spelling)
				}
				want := m.Mask(spelling)
				for _, cut := range []int{1, len(spelling) / 2, len(spelling) - 1} {
					stream := m.Stream()
					got := stream.Mask(spelling[:cut]) + stream.Mask(spelling[cut:]) + stream.Flush()
					if got != want {
						t.Fatalf("raw %q derived %q depth %d cut %d: got %q, want %q", raw, value, depth, cut, got, want)
					}
				}
			}
		}
	}
}

func longestPattern(values []string) int {
	longest := 0
	for _, value := range values {
		longest = max(longest, len(value))
	}
	return longest
}

func liveMasker(b testing.TB) *Masker {
	b.Helper()
	m := NewMasker()
	for _, value := range []string{
		"0123456789abcdefghijklmnopqrstuvwxyzABCDEFG",
		"-----BEGIN PRIVATE KEY-----\n" + strings.Repeat("MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQDb5dJ7j\n", 32) + "-----END PRIVATE KEY-----\n",
	} {
		if err := m.Register(value); err != nil {
			b.Fatal(err)
		}
	}
	return m
}

func TestLiveMaskerPublishesFirstLineWithinOneWrite(t *testing.T) {
	stream := liveMasker(t).Stream()
	if got := stream.Mask("first ordinary output line\n"); got != "first ordinary output line\n" {
		t.Fatalf("first write withheld: %q", got)
	}
}

func TestStreamHoldsOnlyViablePrefix(t *testing.T) {
	stream := newTestMasker("private-token").Stream()
	if got := stream.Mask("visible pri"); got != "visible " {
		t.Fatalf("over-retained: %q", got)
	}
	if got := stream.Mask("nter\n"); got != "printer\n" {
		t.Fatalf("failed prefix not released: %q", got)
	}
}

func TestIdleLineFlushPreservesUnknownSecretTail(t *testing.T) {
	lines := newTestMasker("private-token").Stream().Lines()
	if got := lines.Push("visible pri", false); len(got) != 0 {
		t.Fatal(got)
	}
	if got := lines.Idle(); got != "visible " {
		t.Fatalf("idle = %q", got)
	}
	if got := lines.Push("vate-token\n", false); len(got) != 1 || got[0] != "***" {
		t.Fatalf("continuation = %q", got)
	}
}

func TestIdleLineFlushPreservesIncompleteUTF8(t *testing.T) {
	lines := NewMasker().Stream().Lines()
	lines.Push("safe \xe7", false)
	if got := lines.Idle(); got != "safe " {
		t.Fatalf("idle = %q", got)
	}
	if got := lines.Push("\xa7\x98\n", false); len(got) != 1 || got[0] != "秘" {
		t.Fatalf("UTF-8 continuation = %q", got)
	}
}

func BenchmarkStreamLivePEMFirstLine(b *testing.B) {
	m := liveMasker(b)
	m.matcher()
	const line = "first ordinary output line\n"
	b.ReportAllocs()
	b.SetBytes(int64(len(line)))
	b.ResetTimer()
	for range b.N {
		streamBenchmarkSink = m.Stream().Mask(line)
		if streamBenchmarkSink != line {
			b.Fatal("first line withheld")
		}
	}
}

func BenchmarkStreamLivePEMColdFirstLine(b *testing.B) {
	b.ReportAllocs()
	for range b.N {
		b.StopTimer()
		m := liveMasker(b)
		b.StartTimer()
		streamBenchmarkSink = m.Stream().Mask("first ordinary output line\n")
		if streamBenchmarkSink == "" {
			b.Fatal("first line withheld")
		}
	}
}

func BenchmarkStreamLivePEMLineByLine(b *testing.B) {
	m := liveMasker(b)
	m.matcher()
	stream := m.Stream()
	line := strings.Repeat("ordinary output ", 7) + "line complete\n"
	b.ReportAllocs()
	b.SetBytes(int64(len(line)))
	b.ResetTimer()
	for range b.N {
		streamBenchmarkSink = stream.Mask(line)
	}
}
