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

func TestLineWriterDropsALongLineWhole(t *testing.T) {
	const secret = "fixture-secret-7c41e9fixture"
	expanding := "0123456789" + strings.Repeat("a", 40) + "TAIL"
	for name, tc := range map[string]struct {
		values []string
		writes []string
	}{
		"secret ending at the limit": {[]string{secret}, []string{strings.Repeat("x", maxMaskedLine-len(secret)) + secret + strings.Repeat("y", 64) + "\n"}},
		"secret straddling the cut":  {[]string{secret}, []string{strings.Repeat("x", maxMaskedLine-5) + secret[:12], secret[12:] + "\n"}},
		"secret ending the buffer":   {[]string{secret, "fixture"}, []string{strings.Repeat("x", maxMaskedLine-3) + secret, "rest\n"}},
		"masking that expands text":  {[]string{"a", expanding}, []string{strings.Repeat("a", maxMaskedLine-30) + expanding + "\n"}},
	} {
		t.Run(name, func(t *testing.T) {
			v := knownValues(tc.values...)
			var out bytes.Buffer
			w := v.Writer(&out)
			for _, p := range append(tc.writes, "next line\n") {
				if _, err := w.Write([]byte(p)); err != nil {
					t.Fatal(err)
				}
			}
			v.Close()
			if got := out.String(); !strings.HasPrefix(got, "[line over 1 MiB dropped: ") || !strings.HasSuffix(got, " bytes]\nnext line\n") || len(got) > 80 {
				t.Fatalf("output = %q, want only the drop marker and the next line", got[:min(len(got), 120)])
			}
		})
	}
}

func TestLineWriterMasksALineJustUnderTheLimitWhole(t *testing.T) {
	const secret = "fixture-secret-7c41e9"
	v := knownValues(secret)
	var out bytes.Buffer
	w := v.Writer(&out)
	line := strings.Repeat("x", maxMaskedLine-len(secret)) + secret
	if _, err := w.Write([]byte(line[:600_000])); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(line[600_000:] + "\n")); err != nil {
		t.Fatal(err)
	}
	v.Close()
	if want := strings.Repeat("x", maxMaskedLine-len(secret)) + "***\n"; out.String() != want {
		t.Fatalf("got %d bytes ending %q, want the whole line masked", out.Len(), out.String()[max(0, out.Len()-30):])
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
