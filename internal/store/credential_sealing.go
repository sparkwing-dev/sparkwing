package store

import "github.com/sparkwing-dev/sparkwing/internal/secrets"

// SecretSealer seals values against their complete stored row identity.
type SecretSealer interface {
	SealBound(purpose secrets.Purpose, team, name, scope string, shared, masked bool, plain string) (string, error)
}

// SealedSecret is an immutable tenant-bound secret write.
type SealedSecret struct {
	team Team
	row  Secret
}

// SealedGitCredential is an immutable tenant-bound source credential write.
type SealedGitCredential struct {
	team Team
	row  GitCredential
}

// Row returns the sealed row for inspection.
func (s SealedSecret) Row() Secret { return s.row }

// Row returns the sealed row for inspection.
func (s SealedGitCredential) Row() GitCredential { return s.row }

// SealSecret encrypts a secret for this tenant and its complete access fields.
func SealSecret(cipher SecretSealer, team Team, row Secret) (SealedSecret, error) {
	if cipher == nil {
		return SealedSecret{}, ErrInvalidInput
	}
	value, err := cipher.SealBound(secrets.PurposeSecret, string(team), row.Name, row.Pipeline, row.Shared, row.Masked, row.Value)
	if err != nil {
		return SealedSecret{}, err
	}
	if !secrets.IsBound(value) {
		return SealedSecret{}, ErrInvalidInput
	}
	row.Value = value
	return SealedSecret{team: team, row: row}, nil
}

// SealGitCredential encrypts a source credential for this tenant and host.
func SealGitCredential(cipher SecretSealer, team Team, row GitCredential) (SealedGitCredential, error) {
	if cipher == nil {
		return SealedGitCredential{}, ErrInvalidInput
	}
	value, err := cipher.SealBound(secrets.PurposeGitCredential, string(team), "git-credential:"+row.Host, "git-credential", false, true, row.Secret)
	if err != nil {
		return SealedGitCredential{}, err
	}
	if !secrets.IsBound(value) {
		return SealedGitCredential{}, ErrInvalidInput
	}
	row.Secret = value
	return SealedGitCredential{team: team, row: row}, nil
}
