package orchestrator

import (
	"encoding"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// processBoundaryGuide is where an author reads why a node output has
// to survive a JSON round-trip. Both the plan-time rejection and the
// execution-time marshal failure cite it, so the two messages lead to
// one explanation.
const processBoundaryGuide = "docs/migrations/v0.36.0.md#process-per-node"

// nodeOutputMarshalError is the failure a node gets when its output
// cannot be encoded. A node runs in its own process, so its output
// reaches a downstream node only as JSON in the run store: an output
// that will not marshal has not been produced, and reporting success
// would hand every consumer an empty value instead.
func nodeOutputMarshalError(nodeID string, output any, err error) error {
	return fmt.Errorf(
		"%s: output of type %T could not be encoded as JSON: %w\n"+
			"  a node's output reaches downstream nodes only by JSON round-trip through the run store\n"+
			"  see %s",
		nodeID, output, err, processBoundaryGuide)
}

// validateOutputSerializable reports why a job's declared output type
// can never round-trip through JSON, or nil when it can. It runs at
// plan time so an unencodable output is a rejected plan rather than a
// node that runs its whole body and then fails on the way out.
//
// It rejects only the four kinds encoding/json refuses for every
// value: chan, func, complex, and unsafe.Pointer. Everything else is
// left to the execution-time check on the actual value.
//
// The narrowness is the point. This check runs on the cluster path
// too, where a wrong verdict turns a pipeline that has been shipping
// for months red on upgrade with no way to override it. In
// particular a struct with no exported fields is NOT rejected:
// json.Marshal encodes it as {}, which is a real answer, and
// rejecting it would fail every job whose output embeds a sync.Mutex,
// an *os.File, or any third-party type that keeps its state private.
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

// serializableStruct walks only the fields encoding/json will look
// at. An unexported field is skipped rather than inspected, which is
// also what keeps the walk out of the private innards of types this
// repository does not own.
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

// planOutputTypeErrors collects the plan-time output-type rejections
// for every node that declares one, as a single error naming each
// offending job. Returns nil when the plan is clean.
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
