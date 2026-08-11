package secrets

import (
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// captureLogger records the last record it was handed.
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

// The record that leaked: a failed step reports the command's whole
// output in attrs["error"], which masking Msg alone never touched.
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

// Attribute maps are shared -- run_start hands the same invocation map
// to the log record and to the persisted run row -- so masking must
// copy rather than write through.
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

// Nothing to redact means nothing to copy: the same map comes back.
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

// Attribute values are not always flat strings: slices and nested maps
// carry command output too (argv lists, per-step rollups).
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

// A self-referential map must not spin forever.
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
