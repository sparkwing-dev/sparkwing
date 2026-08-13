// Package ndjson writes CLI list output as newline-delimited JSON:
// one complete, independently parseable object per line.
//
// A caller's only defense against output too large for its context is
// `head`, and `head` is line-oriented. A single-line JSON array
// truncates into invalid JSON and yields nothing at all; a
// pretty-printed array truncates halfway through a record and yields
// the same nothing. NDJSON makes a truncated read lossy but still
// valid, which is the difference between reading the first five
// records and reading none of them. This is house rule 12 of the CLI
// design standard: list output is one record per line, in every mode.
//
// The package is deliberately tiny. It exists so the rule has one
// implementation the whole binary shares rather than a re-derived
// encoder loop at each of the three dozen list verbs, where the
// difference between a stream and an array is one forgotten line.
package ndjson

import (
	"encoding/json"
	"io"
)

// Write streams records as NDJSON, one per line, in the order given.
//
// An empty listing is an empty stream rather than `[]`, because zero
// records is what no lines looks like when records are lines. A
// consumer that treated empty output as an error has to treat it as
// zero records instead.
func Write[T any](w io.Writer, records []T) error {
	enc := json.NewEncoder(w)
	for _, r := range records {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return nil
}
