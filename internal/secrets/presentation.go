package secrets

import (
	"bytes"
	"encoding/json"

	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

// Error sanitizes author payloads before constructing a trusted error envelope.
func (m *Masker) Error(source *apierr.Error) *apierr.Error {
	if source == nil {
		return nil
	}
	code := source.Code
	if m.Mask(string(code)) != string(code) {
		code = apierr.Internal
	}
	result := &apierr.Error{Code: code, Message: m.Mask(source.Message), Hint: m.Mask(source.Hint)}
	if source.Details != nil {
		result.Details = map[string]any{"masked_attrs": "***"}
		if raw, err := json.Marshal(source.Details); err == nil {
			result.Details = nil
			decoder := json.NewDecoder(bytes.NewReader(m.Attributes(raw)))
			decoder.UseNumber()
			if err := decoder.Decode(&result.Details); err != nil {
				result.Details = map[string]any{"masked_attrs": "***"}
			}
		}
	}
	return result
}

// Log sanitizes presentation payloads while retaining the protocol's field names.
func (m *Masker) Log(frame nodeproto.FrameLog) nodeproto.FrameLog {
	frame.TS, frame.Level = m.Mask(frame.TS), m.Mask(frame.Level)
	frame.Step, frame.Event = m.Mask(frame.Step), m.Mask(frame.Event)
	frame.Message = m.Mask(frame.Message)
	if len(frame.Attrs) > 0 {
		frame.Attrs = m.Attributes(frame.Attrs)
	}
	return frame
}
