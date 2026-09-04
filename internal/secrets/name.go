package secrets

import "github.com/sparkwing-dev/sparkwing/internal/secretname"

func ValidateName(name string) error {
	return secretname.Validate(name)
}
