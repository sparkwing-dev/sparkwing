package ndjson

import (
	"encoding/json"
	"io"
)

func Write[T any](w io.Writer, records []T) error {
	enc := json.NewEncoder(w)
	for _, r := range records {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return nil
}
