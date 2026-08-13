package orchestrator

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// decodeNDJSON reads a list renderer's JSON output a line at a time,
// the way a consumer now has to. It fails on the first line that is not
// a complete JSON value, which is the property the change exists to
// provide.
func decodeNDJSON[T any](t *testing.T, out string) []T {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(out))
	var got []T
	for {
		var v T
		err := dec.Decode(&v)
		if err == io.EOF {
			return got
		}
		if err != nil {
			t.Fatalf("decode NDJSON record %d: %v\noutput:\n%s", len(got), err, out)
		}
		got = append(got, v)
	}
}
