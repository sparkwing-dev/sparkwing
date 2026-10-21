package secrets

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
)

func newTestMasker(values ...string) *Masker {
	m := NewMasker()
	for _, v := range values {
		m.Register(v)
	}
	return m
}

func TestMask_OverlappingSecretsLeaveNoTail(t *testing.T) {
	for name, m := range map[string]*Masker{
		"short registered first": newTestMasker("abc", "abcdef"),
		"long registered first":  newTestMasker("abcdef", "abc"),
	} {
		if got := m.Mask("token=abcdef"); got != "token=***" {
			t.Errorf("%s: Mask = %q, want token=***", name, got)
		}
		if got := m.Mask("token=abc"); got != "token=***" {
			t.Errorf("%s: Mask of the short secret = %q, want token=***", name, got)
		}
	}
}

func TestMaskJSONReachesKeysNumbersAndEscapes(t *testing.T) {
	m := newTestMasker("s3cr3t", "1234567", "a<b")
	for raw, want := range map[string]string{
		`{"s3cr3t":"ok","n":3}`:        `{"***":"ok","n":3}`,
		`{"v":"s3cr3t","w":"a<b"}`:     `{"v":"***","w":"***"}`,
		`{"pin":1234567}`:              `{"pin":"***"}`,
		`{ "safe": 9007199254740993 }`: `{"safe":9007199254740993}`,
		`not json s3cr3t`:              `not json ***`,
	} {
		if got := string(m.MaskJSON([]byte(raw))); got != want {
			t.Errorf("MaskJSON(%s) = %s, want %s", raw, got, want)
		}
	}
	var nilMasker *Masker
	if got := string(nilMasker.MaskJSON([]byte(`{"a":1}`))); got != `{"a":1}` {
		t.Errorf("nil masker changed the payload: %s", got)
	}
}

func TestMaskJSONKeepsStructuralKeysBesideMultilineSecrets(t *testing.T) {
	m := newTestMasker("attribute\"sentinel\nvalue")
	merged := NewMasker()
	merged.Merge(m)
	raw := []byte(`{"nested":[{"value":"attribute\"sentinel\nvalue"}],"prefix-attribute\"sentinel\nvalue":true}`)
	want := `{"nested":[{"value":"***"}],"prefix-***":true}`
	for _, masker := range []*Masker{m, merged} {
		if got := string(masker.MaskJSON(raw)); got != want {
			t.Fatalf("MaskJSON = %s, want %s", got, want)
		}
		if got := masker.Mask("value"); got != "***" {
			t.Fatalf("multiline text component = %q", got)
		}
		masker.Register("value")
		if got := string(masker.MaskJSON([]byte(`{"value":"public"}`))); got != `{"***":"public"}` {
			t.Fatalf("explicitly registered key = %s", got)
		}
	}
}

func TestMaskJSONReturnsCanonicalPayload(t *testing.T) {
	for name, m := range map[string]*Masker{
		"registered": newTestMasker("private-token"),
		"empty":      NewMasker(),
		"nil":        nil,
	} {
		t.Run(name, func(t *testing.T) {
			raw := []byte(`{ "x": "private-token", "x": "healthy", "n": 9007199254740993 }`)
			if got := string(m.MaskJSON(raw)); got != `{"n":9007199254740993,"x":"healthy"}` {
				t.Fatalf("uninspected payload returned: %s", got)
			}
		})
	}
}

func TestMaskJSONConcurrentRegistrationKeepsRegisteredSecretHidden(t *testing.T) {
	const secret = "registered\"private\nvalue"
	m := NewMasker()
	m.Register(secret)
	payload, err := json.Marshal(map[string]any{"token": secret, "count": 12345})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := 0; i < 50; i++ {
			m.Register(fmt.Sprintf("other-private-%d", i))
		}
	})
	wg.Go(func() {
		for i := 0; i < 50; i++ {
			var decoded struct {
				Token string
				Count int
			}
			if err := json.Unmarshal(m.MaskJSON(payload), &decoded); err != nil {
				t.Error(err)
				return
			}
			if decoded.Token != "***" || decoded.Count != 12345 {
				t.Error("registered secret or numeric data changed under concurrent registration")
				return
			}
		}
	})
	wg.Wait()
}

// A short secret whose JSON spelling escapes a character is masked in JSON,
// though its other encodings are too short to register.
func TestMaskJSONMasksAShortEscapedSecret(t *testing.T) {
	m := newTestMasker(`a"`)
	if got := string(m.MaskJSON([]byte(`{"token":"a\""}`))); got != `{"token":"***"}` {
		t.Fatalf("MaskJSON = %s", got)
	}
}
