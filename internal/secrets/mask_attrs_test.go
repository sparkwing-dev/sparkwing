package secrets

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type captureLogger struct{ last sparkwing.LogRecord }

func (c *captureLogger) Log(level, msg string) {
	c.Emit(sparkwing.LogRecord{Level: level, Msg: msg})
}
func (c *captureLogger) Emit(rec sparkwing.LogRecord) { c.last = rec }

func newTestMasker(values ...string) *Masker {
	m := NewMasker()
	for _, v := range values {
		m.Register(v)
	}
	return m
}

func TestWrappedLogger_MasksStringAttrs(t *testing.T) {
	inner := &captureLogger{}
	log := MaskingLogger(inner, newTestMasker("s3cr3t"))

	log.Emit(sparkwing.LogRecord{
		Event: "step_end",
		Msg:   "compile",
		Attrs: map[string]any{
			"step":        "compile",
			"duration_ms": 13,
			"error":       "command failed (exit 2): deploy --token s3cr3t\nauth rejected",
		},
	})

	got, _ := inner.last.Attrs["error"].(string)
	if got != "command failed (exit 2): deploy --token ***\nauth rejected" {
		t.Fatalf("attrs[error] = %q", got)
	}
	if inner.last.Attrs["duration_ms"] != 13 {
		t.Fatalf("non-string attr was rewritten: %#v", inner.last.Attrs["duration_ms"])
	}
}

func TestMaskAttrs_DoesNotMutateCallerMap(t *testing.T) {
	m := newTestMasker("s3cr3t")
	attrs := map[string]any{"cmd": "deploy --token s3cr3t"}

	out := m.MaskAttrs(attrs)
	if attrs["cmd"] != "deploy --token s3cr3t" {
		t.Fatalf("caller's map was mutated: %#v", attrs)
	}
	if out["cmd"] != "deploy --token ***" {
		t.Fatalf("returned map not masked: %#v", out)
	}
}

func TestMaskAttrs_ReturnsInputWhenUnchanged(t *testing.T) {
	attrs := map[string]any{"cmd": "deploy --dry-run", "n": 3}

	for name, m := range map[string]*Masker{
		"no values registered": NewMasker(),
		"no value present":     newTestMasker("s3cr3t"),
	} {
		out := m.MaskAttrs(attrs)
		if reflect.ValueOf(out).Pointer() != reflect.ValueOf(attrs).Pointer() {
			t.Fatalf("%s: expected the input map back, got a copy", name)
		}
	}
	if got := (*Masker)(nil).MaskAttrs(attrs); got == nil {
		t.Fatal("nil masker must pass attrs through")
	}
	if got := newTestMasker("s3cr3t").MaskAttrs(nil); got != nil {
		t.Fatalf("nil attrs must stay nil, got %#v", got)
	}
}

func TestMaskAttrs_RecursesIntoContainers(t *testing.T) {
	m := newTestMasker("s3cr3t")
	nested := map[string]any{"headers": map[string]string{"auth": "Bearer s3cr3t"}}
	attrs := map[string]any{
		"argv":  []string{"deploy", "--token", "s3cr3t"},
		"lines": []any{"ok", "token s3cr3t rejected", 7},
		"req":   nested,
		"keep":  []string{"clean"},
	}

	out := m.MaskAttrs(attrs)

	if got := out["argv"].([]string); !reflect.DeepEqual(got, []string{"deploy", "--token", "***"}) {
		t.Fatalf("argv = %#v", got)
	}
	if got := out["lines"].([]any); !reflect.DeepEqual(got, []any{"ok", "token *** rejected", 7}) {
		t.Fatalf("lines = %#v", got)
	}
	req := out["req"].(map[string]any)["headers"].(map[string]string)
	if req["auth"] != "Bearer ***" {
		t.Fatalf("nested header = %q", req["auth"])
	}
	if nested["headers"].(map[string]string)["auth"] != "Bearer s3cr3t" {
		t.Fatal("nested caller map was mutated")
	}
	if !reflect.DeepEqual(attrs["argv"], []string{"deploy", "--token", "s3cr3t"}) {
		t.Fatalf("caller's slice was mutated: %#v", attrs["argv"])
	}
	if reflect.ValueOf(out["keep"].([]string)).Pointer() != reflect.ValueOf(attrs["keep"].([]string)).Pointer() {
		t.Fatal("an untouched slice should not be cloned")
	}
}

func TestMaskAttrs_FailsClosedPastDepthCap(t *testing.T) {
	m := newTestMasker("s3cr3t")

	deep := map[string]any{"leaf": "token s3cr3t here"}
	for range maskAttrsMaxDepth {
		deep = map[string]any{"next": deep}
	}
	out := m.MaskAttrs(deep)

	cur := out
	for hop := range maskAttrsMaxDepth {
		next, ok := cur["next"]
		if !ok {
			t.Fatalf("nesting lost after %d hops: %#v", hop, cur)
		}
		if s, isStr := next.(string); isStr {

			if s != "***" {
				t.Fatalf("uninspected level rendered as %q, want ***", s)
			}
			return
		}
		cur, ok = next.(map[string]any)
		if !ok {
			t.Fatalf("unexpected nested type %T", next)
		}
	}

	leaf := cur["leaf"]
	if leaf != "***" {
		t.Fatalf("value past the depth cap = %#v, want the whole value replaced by ***", leaf)
	}
	if strings.Contains(fmt.Sprint(out), "token") {
		t.Fatalf("surrounding text past the cap survived: %#v", out)
	}
}

func TestMaskAttrs_ShallowValuesUnaffectedByDepthGuard(t *testing.T) {
	m := newTestMasker("s3cr3t")
	attrs := map[string]any{"a": map[string]any{"b": map[string]any{"c": "x s3cr3t"}}}
	out := m.MaskAttrs(attrs)
	got := out["a"].(map[string]any)["b"].(map[string]any)["c"]
	if got != "x ***" {
		t.Fatalf("nested value = %#v, want masked", got)
	}
}

func TestMaskAttrs_BoundedDepth(t *testing.T) {
	m := newTestMasker("s3cr3t")
	cycle := map[string]any{"cmd": "deploy s3cr3t"}
	cycle["self"] = cycle

	done := make(chan map[string]any, 1)
	go func() { done <- m.MaskAttrs(cycle) }()
	out := <-done
	if out["cmd"] != "deploy ***" {
		t.Fatalf("cmd = %#v", out["cmd"])
	}
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

type maskTestPayload struct{ Token string }

func TestMaskAttrs_MasksErrorByteAndUnknownAttrs(t *testing.T) {
	m := newTestMasker("s3cr3t")
	wrapped := errors.New("failed with s3cr3t")
	attrs := map[string]any{
		"err":     fmt.Errorf("deploy: %w", wrapped),
		"body":    []byte("token=s3cr3t"),
		"payload": maskTestPayload{Token: "s3cr3t"},
		"count":   3,
	}

	out := m.MaskAttrs(attrs)

	gotErr, ok := out["err"].(error)
	if !ok {
		t.Fatalf("err attr is %T, want an error", out["err"])
	}
	if gotErr.Error() != "deploy: failed with ***" {
		t.Errorf("err = %q", gotErr.Error())
	}
	if !errors.Is(gotErr, wrapped) {
		t.Error("masking an error broke its unwrap chain")
	}
	if got := out["body"].([]byte); string(got) != "token=***" {
		t.Errorf("body = %q", got)
	}
	if out["count"] != 3 {
		t.Errorf("count = %#v, want the int back unchanged", out["count"])
	}
	if s := fmt.Sprint(out); strings.Contains(s, "s3cr3t") {
		t.Errorf("masked attrs still carry the secret: %s", s)
	}
	if !strings.Contains(fmt.Sprint(attrs), "s3cr3t") {
		t.Error("caller's attrs were mutated")
	}
}

type maskedStringJSONPayload struct {
	Token string `json:"token"`
}

func (maskedStringJSONPayload) String() string { return "credential" }
func TestMaskAttrsMasksJSONRendering(t *testing.T) {
	for _, secret := range []string{"s3cr3t", "token<>&\nvalue"} {
		t.Run(secret, func(t *testing.T) {
			attrs := map[string]any{"cred": maskedStringJSONPayload{Token: secret}}
			output := newTestMasker(secret).MaskAttrs(attrs)
			body, err := json.Marshal(output)
			if err != nil {
				t.Fatal(err)
			}
			var decoded map[string]any
			if err := json.Unmarshal(body, &decoded); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(fmt.Sprint(decoded), secret) {
				t.Fatalf("JSON attrs leaked registered value: %s", body)
			}
			if attrs["cred"].(maskedStringJSONPayload).Token != secret {
				t.Fatal("mutated caller")
			}
		})
	}
}

func TestMaskedErrorKeepsMessageInJSON(t *testing.T) {
	output := newTestMasker("s3cr3t").MaskAttrs(map[string]any{"err": errors.New("failed s3cr3t")})
	body, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"err":"failed ***"}` {
		t.Fatalf("JSON lost masked error: %s", body)
	}
}

type jsonMaskError struct {
	Token string `json:"token"`
}

func (jsonMaskError) Error() string { return "credential rejected" }
func TestMaskAttrsMasksJSONErrorPayload(t *testing.T) {
	original := jsonMaskError{Token: "s3cr3t"}
	output := newTestMasker("s3cr3t").MaskAttrs(map[string]any{"error": original})
	body, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "s3cr3t") {
		t.Fatalf("JSON leaked error payload: %s", body)
	}
	if !errors.Is(output["error"].(error), original) {
		t.Fatal("lost error chain")
	}
}

func TestMaskAttrsJSONPreservesNumbers(t *testing.T) {
	raw := json.RawMessage(`{"token":"s3cr3t","id":9007199254740993}`)
	output := newTestMasker("s3cr3t").MaskAttrs(map[string]any{"raw": raw})
	body, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"id":9007199254740993`) || strings.Contains(string(body), "s3cr3t") {
		t.Fatalf("JSON changed numeric value or leaked: %s", body)
	}
}
