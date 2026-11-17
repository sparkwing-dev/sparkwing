package store

import (
	"encoding/json"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

const planArgumentsScope = "plan"

func nodeArgumentsScope(node string) string          { return "node:" + node }
func expansionBodyArgumentsScope(node string) string { return "expansion-body:" + node }
func scheduleArgumentsScope(column string) string    { return "schedule:" + column }
func fireArgumentsScope(schedule string) string      { return "schedule-fire:" + schedule }

// safety: keeping the original bytes preserves unknown fields and the plaintext hashes used by replay.
type argumentBody struct {
	Version int    `json:"argument_document_version"`
	Body    string `json:"body"`
}

func bodyArgumentSlots(body *argumentBody) argumentSlots {
	return argumentSlots{strings: []*string{&body.Body}}
}

func encodeArgumentBody(cipher SessionKeyCipher, team Team, name, scope string, raw []byte) ([]byte, error) {
	if len(raw) == 0 || string(raw) == "null" || storesPlaintextArguments(cipher) {
		return raw, nil
	}
	stored, _, err := encodeRunDocument(cipher, team, name, scope, argumentBody{Version: 1, Body: string(raw)}, bodyArgumentSlots)
	return stored, err
}

func decodeArgumentBody(s *Store, team Team, name, scope string, raw []byte) ([]byte, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return raw, nil
	}
	envelope, err := bodyArgumentsEnvelope(raw)
	if err != nil {
		return nil, corruptRunArguments()
	}
	if envelope == "" {
		return raw, nil
	}
	var body argumentBody
	if err := decodeRunDocument(s, team, name, scope, raw, &body, bodyArgumentSlots); err != nil {
		return nil, err
	}
	if !json.Valid([]byte(body.Body)) {
		return nil, corruptRunArguments()
	}
	return []byte(body.Body), nil
}

func encodeScheduleDocument(s *Store, team Team, id, column string, raw []byte) ([]byte, error) {
	return encodeArgumentBody(s.runEnvCipher, team, id, scheduleArgumentsScope(column), raw)
}

func decodeScheduleDocument(s *Store, team Team, id, column string, raw []byte) ([]byte, error) {
	return decodeArgumentBody(s, team, id, scheduleArgumentsScope(column), raw)
}

func hasJobArgumentDefaults(raw []byte) bool {
	type job struct {
		Arguments []struct {
			Default *string `json:"default"`
		} `json:"arguments"`
	}
	var document struct {
		job
		Jobs []job `json:"jobs"`
	}
	if json.Unmarshal(raw, &document) != nil {
		return true
	}
	for _, j := range append(document.Jobs, document.job) {
		for _, argument := range j.Arguments {
			if argument.Default != nil && *argument.Default != "" {
				return true
			}
		}
	}
	return false
}

func encodeJobArgumentBody(s *Store, team Team, run, scope string, raw []byte) ([]byte, error) {
	if !hasJobArgumentDefaults(raw) {
		return raw, nil
	}
	return encodeArgumentBody(s.runEnvCipher, team, run, scope, raw)
}

// safety: argument maps contain only string values, so a numeric version cannot be a user argument.
func bodyArgumentsEnvelope(raw []byte) (string, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return "", corruptRunArguments()
	}
	marker, ok := fields["argument_document_version"]
	if !ok || len(marker) > 0 && marker[0] == '"' {
		return "", nil
	}
	var version int
	var body, envelope string
	if json.Unmarshal(marker, &version) != nil || version != 1 || len(fields) != 3 || json.Unmarshal(fields["body"], &body) != nil || body != "" || json.Unmarshal(fields[sealedArgumentsKey], &envelope) != nil || !secrets.IsBound(envelope) {
		return "", corruptRunArguments()
	}
	return envelope, nil
}
