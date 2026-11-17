package store

import (
	"encoding/json"
)

type nodeArgumentDefault struct {
	Default *string `json:"default,omitempty"`
}
type nodeArgumentDocument struct {
	Arguments []nodeArgumentDefault `json:"arguments,omitempty"`
}

func nodeArgumentSlots(job *nodeArgumentDocument) argumentSlots {
	var slots argumentSlots
	for i := range job.Arguments {
		if value := job.Arguments[i].Default; value != nil {
			slots.strings = append(slots.strings, value)
		}
	}
	return slots
}

func encodeNodeSpec(cipher SessionKeyCipher, team Team, run, node string, raw []byte) ([]byte, error) {
	var job nodeArgumentDocument
	if json.Unmarshal(raw, &job) != nil {
		return nil, corruptRunArguments()
	}
	stored, sealed, err := encodeRunDocument(cipher, team, run, nodeArgumentsScope(node), job, nodeArgumentSlots)
	if err != nil || !sealed {
		return raw, err
	}
	envelope, err := sealedArgumentsEnvelope(stored)
	if err != nil {
		return nil, corruptRunArguments()
	}
	for i := range job.Arguments {
		if job.Arguments[i].Default != nil {
			value := ""
			job.Arguments[i].Default = &value
		}
	}
	return replaceNodeDefaults(raw, job.Arguments, envelope)
}

func decodeNodeSpec(s *Store, team Team, run, node string, raw []byte) ([]byte, error) {
	envelope, err := sealedArgumentsEnvelope(raw)
	if err != nil {
		return nil, corruptRunArguments()
	}
	if envelope == "" {
		return raw, nil
	}
	var job nodeArgumentDocument
	if err := decodeRunDocument(s, team, run, nodeArgumentsScope(node), raw, &job, nodeArgumentSlots); err != nil {
		if runArgumentsUnreadable(err) {
			return nil, err
		}
		return nil, corruptRunArguments()
	}
	return replaceNodeDefaults(raw, job.Arguments, "")
}

// safety: only default fields change; unknown metadata and large JSON integers remain hash-bound as written.
func replaceNodeDefaults(raw []byte, defaults []nodeArgumentDefault, envelope string) ([]byte, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil, corruptRunArguments()
	}
	var arguments []map[string]json.RawMessage
	if json.Unmarshal(fields["arguments"], &arguments) != nil || len(arguments) != len(defaults) {
		return nil, corruptRunArguments()
	}
	for i, declaration := range defaults {
		if declaration.Default != nil {
			value, err := json.Marshal(*declaration.Default)
			if err != nil {
				return nil, err
			}
			arguments[i]["default"] = value
		}
	}
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return nil, err
	}
	fields["arguments"] = encoded
	delete(fields, sealedArgumentsKey)
	if envelope != "" {
		value, err := json.Marshal(envelope)
		if err != nil {
			return nil, err
		}
		fields[sealedArgumentsKey] = value
	}
	return json.Marshal(fields)
}

func (r *sealedRotationTx) nodeArguments(team Team, run, node string, raw []byte) ([]byte, error) {
	envelope, err := sealedArgumentsEnvelope(raw)
	if err != nil {
		return nil, corruptRunArguments()
	}
	if envelope != "" {
		return rotateRunDocument(r, team, run, nodeArgumentsScope(node), raw, nodeArgumentSlots)
	}
	stored, err := encodeNodeSpec(r.cipher, team, run, node, raw)
	if err != nil {
		return nil, err
	}
	sealed, err := sealedArgumentsEnvelope(stored)
	if err != nil {
		return nil, err
	}
	if sealed == "" {
		return nil, nil
	}
	r.out.PlaintextRunArguments++
	if r.verify {
		return nil, nil
	}
	return stored, nil
}
