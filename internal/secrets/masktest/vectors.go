// Package masktest loads the masking contract's shared conformance cases.
package masktest

import (
	"bytes"
	"encoding/json"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

// Vector describes registrations and their expected text and JSON projections.
type Vector struct {
	Name                string
	Values, MergeValues []string
	Input, Want         string
	JSONInput, JSONWant string
}

// Load rejects unknown fields so sinks cannot silently ignore new registrations.
func Load(path string) ([]Vector, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var vectors []Vector
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	err = decoder.Decode(&vectors)
	return vectors, err
}

// Masker applies the vector's raw and merged registrations to a fresh registry.
func (v Vector) Masker() *secrets.Masker {
	m, merged := secrets.NewMasker(), secrets.NewMasker()
	for _, value := range v.MergeValues {
		merged.Register(value)
	}
	m.Merge(merged)
	for _, value := range v.Values {
		m.Register(value)
	}
	return m
}
