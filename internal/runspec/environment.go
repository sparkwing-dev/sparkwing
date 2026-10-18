package runspec

import (
	"errors"
	"slices"
)

// EnvironmentSource selects private snapshot inheritance or encrypted submission.
type EnvironmentSource string

const (
	EnvironmentCaptured  EnvironmentSource = "captured"
	EnvironmentSubmitted EnvironmentSource = "submitted"
)

// VerifyEnvironment checks private byte-preserving values against their immutable identity.
func VerifyEnvironment(expected EnvSnapshot, values map[string][]byte) error {
	strings := make(map[string]string, len(values))
	for name, value := range values {
		strings[name] = string(value)
	}
	actual, err := EnvironmentDigest(strings, expected.Salt)
	if err != nil || actual.Digest != expected.Digest || !slices.Equal(actual.Names, expected.Names) {
		return errors.New("submitted environment differs from its sealed identity")
	}
	return nil
}
