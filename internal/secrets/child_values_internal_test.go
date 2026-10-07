package secrets

import (
	"bytes"
	"strings"
	"sync"
	"testing"
)

func knownValues(values ...string) *ChildValues {
	v := &ChildValues{masker: NewMasker(), done: true}
	v.cond = sync.NewCond(&v.mu)
	for _, value := range values {
		v.masker.Register(value)
	}
	return v
}

func TestLineWriterMasksASecretAcrossALongLinesChunkBoundary(t *testing.T) {
	const secret = "chunk-boundary-secret-91d2"
	filler := strings.Repeat("x", maxMaskedLine)
	for name, writes := range map[string][]string{
		"split between writes": {filler + secret[:6], secret[6:] + " tail"},
		"whole inside the cut": {filler + secret + "yy", " tail"},
	} {
		t.Run(name, func(t *testing.T) {
			v := knownValues(secret)
			var out bytes.Buffer
			w := v.Writer(&out)
			for _, p := range writes {
				if _, err := w.Write([]byte(p)); err != nil {
					t.Fatal(err)
				}
			}
			v.Close()
			got := out.String()
			if strings.Contains(got, secret[:6]) || strings.Contains(got, secret[6:]) {
				t.Fatalf("a piece of the secret reached the destination: ...%q", got[max(0, len(got)-80):])
			}
			if !strings.HasSuffix(got, "*** tail") && !strings.HasSuffix(got, "***yy tail") {
				t.Fatalf("tail = %q, want the secret masked in place", got[max(0, len(got)-80):])
			}
			if strings.Count(got, "x") != maxMaskedLine {
				t.Fatalf("got %d filler bytes, want %d", strings.Count(got, "x"), maxMaskedLine)
			}
		})
	}
}

func TestLineWriterKeepsAMatchWholeInsideALongRunOfAnOverlappingValue(t *testing.T) {
	const secret = "fixture-secret-7c41e9a"
	pad := strings.Repeat("a", (maxMaskedLine+2-len(secret))/2)
	v := knownValues("aaaa", secret)
	var out bytes.Buffer
	w := v.Writer(&out)
	if _, err := w.Write([]byte(pad + secret + pad + "aa")); err != nil {
		t.Fatal(err)
	}
	v.Close()
	if strings.Contains(out.String(), "fixture") {
		t.Fatal("part of the secret reached the destination")
	}
}

func TestLineWriterKeepsASecretWholeAcrossConsecutiveFlushes(t *testing.T) {
	const secret = "fixture-secret-7c41e9"
	filler := strings.Repeat("x", maxMaskedLine)
	v := knownValues(secret)
	var out bytes.Buffer
	w := v.Writer(&out)
	cuts := []int{3, 9, 15, 20}
	prev := 0
	for _, c := range cuts {
		if _, err := w.Write([]byte(secret[prev:] + filler + secret[:c])); err != nil {
			t.Fatal(err)
		}
		prev = c
	}
	if _, err := w.Write([]byte(secret[prev:] + "\n")); err != nil {
		t.Fatal(err)
	}
	v.Close()
	got := out.String()
	if strings.Contains(got, "fixture") || strings.Contains(got, "7c41e9") {
		t.Fatal("part of the secret reached the destination")
	}
	if n := strings.Count(got, "***"); n != len(cuts)+1 {
		t.Fatalf("masked %d secrets, want %d", n, len(cuts)+1)
	}
	if want := len(cuts)*len(filler) + (len(cuts)+1)*len("***") + 1; len(got) != want {
		t.Fatalf("got %d bytes, want %d", len(got), want)
	}
}

func TestChildValuesMaskJSONKeepsKeysAndMasksNumbers(t *testing.T) {
	v := knownValues("msg", "12345678")
	got := string(v.MaskJSON([]byte(`{"msg":"a msg","attrs":{"pin":12345678,"n":7}}`)))
	if got != `{"attrs":{"n":7,"pin":"***"},"msg":"a ***"}` {
		t.Fatalf("MaskJSON = %s", got)
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
