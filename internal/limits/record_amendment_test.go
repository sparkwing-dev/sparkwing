package limits_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

func TestRecordPreservesTextWithinLimit(t *testing.T) {
	for _, text := range []string{"", "summary: [MASKED] ✓", strings.Repeat("x", int(limits.AnnotationBytes))} {
		if got := limits.Record(text); got != text {
			t.Fatalf("Record changed text within limit (%d bytes)", len(text))
		}
	}
}

func TestRecordElidesMiddleWithinUTF8ByteLimit(t *testing.T) {
	for _, unit := range []string{"x", "é", "界", "🐦"} {
		t.Run(unit, func(t *testing.T) {
			text := "summary [MASKED]\n" + strings.Repeat(unit, int(limits.AnnotationBytes)) + "\nfinal result [MASKED]"
			got := limits.Record(text)
			if int64(len(got)) > limits.AnnotationBytes || !utf8.ValidString(got) {
				t.Fatalf("Record produced %d bytes, valid UTF-8: %v", len(got), utf8.ValidString(got))
			}
			if !strings.HasPrefix(got, "summary [MASKED]\n") || !strings.HasSuffix(got, "\nfinal result [MASKED]") {
				t.Fatal("Record lost the head or tail")
			}
			if !strings.Contains(got, "elided") {
				t.Fatal("Record omitted the elision note")
			}
		})
	}
}
