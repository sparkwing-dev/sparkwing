package secrets

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestAttributesMasksSerializedJSON(t *testing.T) {
	m := NewMasker()
	if err := m.Register("private-token"); err != nil {
		t.Fatal(err)
	}
	for input, want := range map[string]string{
		`{"token":"private-token"}`:              `{"token":"***"}`,
		`{"private-token":1}`:                    `{"***":1}`,
		`{"a":"ordinary"}`:                       `{"a":"ordinary"}`,
		`{"private-token":1,"***":2}`:            maskedAttributes,
		`{"nested":{"private-token":1,"***":2}}`: maskedAttributes,
		`{"broken":"private-token}`:              maskedAttributes,
		`["private-token"]`:                      maskedAttributes,
	} {
		if got := string(m.Attributes([]byte(input))); got != want {
			t.Errorf("Attributes(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestAttributesRejectsDuplicatesAndInvalidShapeWithoutRegistrations(t *testing.T) {
	m := NewMasker()
	for _, input := range []string{`{"a":1,"a":2}`, `{"a":{"b":1,"b":2}}`, `{"a":[{"b":1,"b":2}]}`, `null`, `{"a":1} true`} {
		if got := string(m.Attributes([]byte(input))); got != maskedAttributes {
			t.Errorf("Attributes(%q) = %q", input, got)
		}
	}
	if got := string(m.Attributes([]byte(`{"a":1}`))); got != `{"a":1}` {
		t.Fatal(got)
	}
}

func TestAttributesFallsBackWhenMaskingBreaksJSONSyntax(t *testing.T) {
	m := NewMasker()
	if err := m.Register("1234"); err != nil {
		t.Fatal(err)
	}
	if got := string(m.Attributes([]byte(`{"n":1234}`))); got != maskedAttributes {
		t.Fatal(got)
	}
	if strings.Contains(string(m.Attributes([]byte(`{"n":1234}`))), "1234") {
		t.Fatal("secret survived")
	}
}

func TestAttributesHidesEscapedSecretsAfterJSONDecode(t *testing.T) {
	m := NewMasker()
	if err := m.Register("private"); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"value":"p\u0072iv\u0061te"}`,
		`{"p\u0072ivate":"ok"}`,
		`{"nested":[{"value":"p\u0072iv\u0061te"}]}`,
		`{"value":"p\\u0072ivate"}`,
	} {
		sanitized := m.Attributes([]byte(raw))
		var decoded any
		if err := json.Unmarshal(sanitized, &decoded); err != nil {
			t.Fatalf("%s: %v", sanitized, err)
		}
		if strings.Contains(fmt.Sprint(decoded), "private") {
			t.Fatalf("decoded leak: %s -> %#v", raw, decoded)
		}
		if strings.Contains(fmt.Sprint(decoded), `p\u0072ivate`) {
			t.Fatalf("escaped spelling survived: %s -> %#v", raw, decoded)
		}
	}
}

func TestAttributesMasksDoubleEscapedOverlap(t *testing.T) {
	m := NewMasker()
	if err := m.Register("abababababab"); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"nested": []string{`abababababab\u0061babababab`}})
	if err != nil {
		t.Fatal(err)
	}
	sanitized := m.Attributes(raw)
	var decoded map[string][]string
	if err := json.Unmarshal(sanitized, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded["nested"]) != 1 || decoded["nested"][0] != "***" {
		t.Fatalf("escaped overlap survived: %s -> %#v", sanitized, decoded)
	}
}
