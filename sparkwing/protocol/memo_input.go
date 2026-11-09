package nodeproto

import (
	"bytes"
	"encoding/json"
)

// UnmarshalJSON preserves literal numbers through plan persistence and replay.
func (input *MemoInput) UnmarshalJSON(raw []byte) error {
	type wire MemoInput
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	return decoder.Decode((*wire)(input))
}
