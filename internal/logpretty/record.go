package logpretty

import "time"

// Record is a persisted log envelope consumed by the terminal renderer.
type Record struct {
	TS    time.Time      `json:"ts"`
	Level string         `json:"level,omitempty"`
	JobID string         `json:"job,omitempty"`
	Step  string         `json:"step,omitempty"`
	Event string         `json:"event,omitempty"`
	Msg   string         `json:"msg,omitempty"`
	Attrs map[string]any `json:"attrs,omitempty"`
}
