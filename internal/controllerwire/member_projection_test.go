package controllerwire

import (
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestMemberReceiptCannotCarryExecutionInputs(t *testing.T) {
	private := map[reflect.Type]bool{reflect.TypeFor[runspec.Request](): true, reflect.TypeFor[runspec.SealedRequest](): true, reflect.TypeFor[runspec.Spec](): true, reflect.TypeFor[runspec.EnvSnapshot](): true}
	seen := map[reflect.Type]bool{}
	var visit func(reflect.Type)
	visit = func(typ reflect.Type) {
		if private[typ] {
			t.Fatalf("member receipt contains private type %s", typ)
		}
		if seen[typ] {
			return
		}
		seen[typ] = true
		switch typ.Kind() {
		case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Array:
			visit(typ.Elem())
		case reflect.Struct:
			for i := range typ.NumField() {
				visit(typ.Field(i).Type)
			}
		}
	}
	visit(reflect.TypeFor[CreateRunResponse]())
}
