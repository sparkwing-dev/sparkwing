package source

import (
	"reflect"
	"testing"
)

func TestSamePreparedSourceCoversEverySpecField(t *testing.T) {
	// safety: samePreparedSource lists fields by hand; a new Spec field it skips
	// would let a stale prepared pipeline restore.
	if got := reflect.TypeFor[Spec]().NumField(); got != 13 {
		t.Fatalf("Spec has %d fields; update samePreparedSource (and preparedSource) for the new field, then this count", got)
	}
}
