package secrets

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func knownValues(values ...string) *ChildValues {
	v := &ChildValues{masker: NewMasker(), done: true}
	v.cond = sync.NewCond(&v.mu)
	for _, value := range values {
		v.masker.Register(value)
	}
	return v
}

// safety: an 8-byte piece already narrows a secret, so none may reach the output.
func assertNoFragment(t *testing.T, out, secret string) {
	t.Helper()
	for n := 8; n <= len(secret); n++ {
		for i := 0; i+n <= len(secret); i++ {
			if strings.Contains(out, secret[i:i+n]) {
				t.Fatalf("output holds %q, a piece of the secret", secret[i:i+n])
			}
		}
	}
}

func TestLineWriterTruncatesALongLineWithoutLeakingASecret(t *testing.T) {
	const secret = "fixture-secret-7c41e9fixture"
	for name, writes := range map[string][]string{
		"secret ending at the limit": {strings.Repeat("x", maxMaskedLine-len(secret)) + secret + strings.Repeat("y", 64) + "\n"},
		"secret straddling the cut":  {strings.Repeat("x", maxMaskedLine-5) + secret[:12], secret[12:] + "\n"},
		"secret ending the buffer":   {strings.Repeat("x", maxMaskedLine-3) + secret, "rest\n"},
	} {
		t.Run(name, func(t *testing.T) {
			v := knownValues(secret, "fixture")
			var out bytes.Buffer
			w := v.Writer(&out)
			for _, p := range append(writes, "next line\n") {
				if _, err := w.Write([]byte(p)); err != nil {
					t.Fatal(err)
				}
			}
			v.Close()
			got := out.String()
			assertNoFragment(t, got, secret)
			if !strings.Contains(got, " bytes]\nnext line\n") || strings.Count(got, "[truncated ") != 1 {
				t.Fatalf("tail = %q, want one truncation marker then the next line", got[max(0, len(got)-60):])
			}
		})
	}
}

func TestLineWriterMasksAnUnterminatedTailAtClose(t *testing.T) {
	const secret = "fixture-secret-7c41e9"
	v := knownValues(secret)
	var out bytes.Buffer
	w := v.Writer(&out)
	if _, err := w.Write([]byte("a " + secret + "\nb " + secret)); err != nil {
		t.Fatal(err)
	}
	v.Close()
	if got := out.String(); got != "a ***\nb ***" {
		t.Fatalf("output = %q", got)
	}
}

func TestChildValuesMaskRecordMasksEveryStringField(t *testing.T) {
	v := knownValues("fixture-secret-7c41e9")
	rec := v.MaskRecord(sparkwing.LogRecord{
		Level: "info", Step: "fixture-secret-7c41e9", Msg: "ok",
		Attrs: map[string]any{"k": "fixture-secret-7c41e9", "n": 7.0},
	})
	if rec.Step != "***" || rec.Msg != "ok" || rec.Attrs["k"] != "***" || rec.Attrs["n"] != 7.0 {
		t.Fatalf("MaskRecord = %+v", rec)
	}
}

func TestRegisterSharesAValueEqualToAnEarlierValuesEncoding(t *testing.T) {
	var shared bytes.Buffer
	shareMu.Lock()
	shareTo = &shared
	shareMu.Unlock()
	t.Cleanup(func() {
		shareMu.Lock()
		shareTo = nil
		shareMu.Unlock()
	})
	m := NewMasker()
	first := "first-secret-value"
	encoded := encodedForms(first)[len(encodedForms(first))-1]
	m.Register(first)
	m.Register(encoded)
	m.Register(first)

	sent := shared.String()
	shareMu.Lock()
	shareTo = nil
	shareMu.Unlock()
	launcher := &ChildValues{masker: NewMasker()}
	launcher.take([]byte(sent))
	for _, form := range encodedForms(encoded) {
		if got := launcher.masker.Mask(form); got != "***" {
			t.Errorf("launcher left %q unmasked (%q); it never learned the second value", form, got)
		}
	}
	if lines := strings.Count(sent, "\n"); lines != 2 {
		t.Errorf("shared %d values, want 2 (each original once)", lines)
	}
}
