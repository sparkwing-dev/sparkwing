package secrets

import "github.com/sparkwing-dev/sparkwing/pkg/secretname"

func ValidateName(name string) error {
	return secretname.Validate(name)
}
