package controllerwire

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

type localScheduleRegistrationJSON LocalScheduleRegistration

type localScheduleRegistrationEnvironment struct {
	*localScheduleRegistrationJSON
	Environment [][]byte `json:"environment"`
}

// MarshalJSON preserves native environment bytes across the local transport.
func (input LocalScheduleRegistration) MarshalJSON() ([]byte, error) {
	encoded := make([][]byte, 0, len(input.Environment))
	for _, entry := range input.Environment {
		encoded = append(encoded, []byte(entry))
	}
	value := localScheduleRegistrationJSON(input)
	return json.Marshal(localScheduleRegistrationEnvironment{&value, encoded})
}

// UnmarshalJSON refuses undeclared fields while preserving captured environment bytes.
func (input *LocalScheduleRegistration) UnmarshalJSON(data []byte) error {
	var value localScheduleRegistrationJSON
	encoded := localScheduleRegistrationEnvironment{localScheduleRegistrationJSON: &value}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&encoded); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err != nil {
			return err
		}
		return errors.New("multiple schedule registration values")
	}
	value.Environment = make([]string, 0, len(encoded.Environment))
	for _, entry := range encoded.Environment {
		value.Environment = append(value.Environment, string(entry))
	}
	*input = LocalScheduleRegistration(value)
	return nil
}
