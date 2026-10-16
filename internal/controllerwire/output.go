package controllerwire

import "encoding/json"

// JobOutput carries the verified JSON bytes of a committed output record.
type JobOutput struct {
	Output json.RawMessage `json:"output"`
}
