package nodeproto

import "encoding/json"

// HelperRequest invokes host behavior with the pipeline process's environment.
type HelperRequest struct {
	Step      string          `json:"step,omitempty"`
	Operation string          `json:"operation"`
	Dir       string          `json:"dir"`
	Env       []string        `json:"env"`
	Args      json.RawMessage `json:"args"`
}

// HelperResponse carries the typed result or a classified host failure.
type HelperResponse struct {
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error,omitempty"`
	Code   string          `json:"code,omitempty"`
}
