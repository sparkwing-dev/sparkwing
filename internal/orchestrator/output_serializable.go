package orchestrator

import (
	"encoding"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const processBoundaryGuide = "docs/migrations/v0.36.0.md#process-per-node"

func nodeOutputMarshalError(nodeID string, output any, err error) error {
	return fmt.Errorf(
		"%s: output of type %T could not be encoded as JSON: %w\n"+
			"  a node's output reaches downstream nodes only by JSON round-trip through the run store\n"+
			"  see %s",
		nodeID, output, err, processBoundaryGuide)
}

func validateOutputSerializable(t reflect.Type) error {
	return serializable(t, map[reflect.Type]bool{})
}

func serializable(t reflect.Type, seen map[reflect.Type]bool) error {
	if t == nil || seen[t] {
		return nil
	}
	seen[t] = true

	// safety: a custom encoder answers for the whole type, including shapes the
	// structural rules below would reject (time.Time has no exported
	// fields and marshals fine).
	if hasCustomJSONEncoding(t) {
		return nil
	}

	switch t.Kind() {
	case reflect.Chan:
		return fmt.Errorf("%s is a channel", t)
	case reflect.Func:
		return fmt.Errorf("%s is a func", t)
	case reflect.Complex64, reflect.Complex128:
		return fmt.Errorf("%s is a complex number", t)
	case reflect.UnsafePointer:
		return fmt.Errorf("%s is an unsafe.Pointer", t)
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return serializable(t.Elem(), seen)
	case reflect.Map:
		if err := serializable(t.Key(), seen); err != nil {
			return fmt.Errorf("map key: %w", err)
		}
		return serializable(t.Elem(), seen)
	case reflect.Struct:
		return serializableStruct(t, seen)
	}
	return nil
}

func serializableStruct(t reflect.Type, seen map[reflect.Type]bool) error {
	for i := range t.NumField() {
		f := t.Field(i)
		if f.PkgPath != "" && !f.Anonymous {
			continue // safety: unexported, so encoding/json ignores it
		}
		if f.Tag.Get("json") == "-" {
			continue
		}
		if err := serializable(f.Type, seen); err != nil {
			return fmt.Errorf("field %s: %w", f.Name, err)
		}
	}
	return nil
}

var (
	jsonMarshaler = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	textMarshaler = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
)

func hasCustomJSONEncoding(t reflect.Type) bool {
	ptr := reflect.PointerTo(t)
	return t.Implements(jsonMarshaler) || ptr.Implements(jsonMarshaler) ||
		t.Implements(textMarshaler) || ptr.Implements(textMarshaler)
}

func planOutputTypeErrors(plan *sparkwing.Plan) error {
	var problems []string
	for _, n := range plan.Nodes() {
		for _, node := range []*sparkwing.JobNode{n, n.OnFailureNode()} {
			if node == nil {
				continue
			}
			t := node.OutputType()
			if t == nil {
				continue
			}
			if err := validateOutputSerializable(t); err != nil {
				problems = append(problems,
					fmt.Sprintf("  job %q returns %s: %v", node.ID(), t, err))
			}
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf(
		"pipeline declares job outputs that cannot cross a process boundary:\n%s\n"+
			"  a node's output reaches downstream nodes only by JSON round-trip through the run store\n"+
			"  see %s",
		strings.Join(problems, "\n"), processBoundaryGuide)
}
