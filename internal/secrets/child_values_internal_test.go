package secrets

import (
	"bytes"
	"encoding/json"
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

func TestChildValuesMaskJSONKeepsNumbersExact(t *testing.T) {
	v := knownValues("pässwörd")
	for _, doc := range []string{
		`{"attrs":{"sequence":9007199254740993},"password":"pässwörd"}`,
		`{"attrs":{"sequence":9007199254740993},"password":"p\u00e4ssw\u00f6rd"}`,
	} {
		got := string(v.MaskJSON([]byte(doc)))
		if got != `{"attrs":{"sequence":9007199254740993},"password":"***"}` {
			t.Errorf("MaskJSON(%s) = %s", doc, got)
		}
	}
}

func TestChildValuesMaskRecordMasksANumberEqualToAValue(t *testing.T) {
	rec := func(n string) sparkwing.LogRecord {
		return sparkwing.LogRecord{Msg: "ok", Attrs: map[string]any{
			"pin": json.Number(n), "list": []any{json.Number(n)}, "n": json.Number("7"),
		}}
	}
	for _, tc := range []struct {
		value, number string
		masked        any
	}{
		{"123456", "123456", true},
		{"123456", "123456.0", true},
		{"123456", "1.23456e5", true},
		{"header\n123456\nfooter", "123456", true},
		{"1234567", "123456", false},
		{"123456", "1e999999999", false},
		{"1e401", "1e401", true},
		{"1e999999999", "1e999999999", true},
		{"1e401", "1E401", false},
		{"1234", "12345", "***5"},
		{"1234", "1.2345e4", false},
	} {
		got := knownValues(tc.value).MaskRecord(rec(tc.number))
		want := any(json.Number(tc.number))
		switch m := tc.masked.(type) {
		case bool:
			if m {
				want = "***"
			}
		case string:
			want = m
		}
		if got.Attrs["pin"] != want || got.Attrs["list"].([]any)[0] != want || got.Attrs["n"] != json.Number("7") {
			t.Errorf("value %q, number %s: attrs = %#v", tc.value, tc.number, got.Attrs)
		}
	}
}

func TestChildValuesMaskRecordMatchesAVeryLongNumberByTextOnly(t *testing.T) {
	long := "0." + strings.Repeat("7", 1_048_000)
	if _, ok := parseNumber(long); ok {
		t.Fatal("a 1 MB number was parsed as a rational")
	}
	v := knownValues("123456", long)
	got := v.MaskRecord(sparkwing.LogRecord{Attrs: map[string]any{"a": json.Number(long), "b": json.Number("0." + strings.Repeat("7", 1_047_999))}})
	if got.Attrs["a"] != "***" || got.Attrs["b"] == "***" {
		t.Fatalf("a masked = %t, b masked = %t; want only the registered spelling masked", got.Attrs["a"] == "***", got.Attrs["b"] == "***")
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
