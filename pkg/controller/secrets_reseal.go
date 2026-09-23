package controller

import (
	"context"
	"errors"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const (
	secretResealBatch = 200
	// safety: enough envelopes that a few corrupt rows cannot pass for a wrong key.
	secretKeyProbeSample = 16
)

// safety: pre-team envelopes were only ever written into the default team, so
// one found in another team was copied there and resealing it would launder
// another team's value into this one.
var errLegacyOutsideDefaultTeam = errors.New(
	"secrets cipher: an envelope sealed before team binding is only trusted in the default team")

// safety: the rotate route and the startup reseal share this, so a row either
// path writes back is sealed to the team it was read from.
func (s *Server) resealStoredSecret(team store.Team, sec store.Secret) (string, error) {
	binding := bindingForRow(team, &sec)
	plain := sec.Value
	switch {
	case secrets.IsBound(sec.Value):
		opened, err := openSecret(s.secretsCipher, binding, sec.Value)
		if err != nil {
			return "", err
		}
		plain = opened
	case secrets.IsEncrypted(sec.Value):
		if team != store.DefaultTeam {
			return "", errLegacyOutsideDefaultTeam
		}
		opened, err := openLegacySecret(s.secretsCipher, binding, sec.Value)
		if err != nil {
			return "", err
		}
		plain = opened
	}
	return sealSecret(s.secretsCipher, binding, plain)
}

// ResealStoredSecrets brings every stored secret under the configured key and
// team binding: a plaintext row, written while the controller ran without a
// key, is sealed, and an envelope from before team binding is resealed with
// its team. It runs before the controller serves, so no reader ever sees a
// row in between, and a second run finds nothing to do.
//
// It refuses, before writing anything, when the key opens none of a sample of
// the envelopes already stored: that key is not the one the table was sealed
// under, and sealing plaintext with it would leave rows only the wrong key
// opens. A row that alone does not open is logged and left as it is.
//
// Without a cipher it does nothing; a cipher that does not bind rows cannot
// seal to a team, so it is refused.
func (s *Server) ResealStoredSecrets(ctx context.Context) (store.SecretResealCounts, error) {
	if s.secretsCipher == nil {
		return store.SecretResealCounts{}, nil
	}
	if _, ok := s.secretsCipher.(BoundCipher); !ok {
		return store.SecretResealCounts{}, errors.New("secrets cipher does not bind values to their row, so stored secrets cannot be sealed to their team")
	}
	if err := s.probeSecretsKey(ctx); err != nil {
		return store.SecretResealCounts{}, err
	}
	counts, err := s.store.ResealSecrets(ctx, secrets.BoundPrefix, secretResealBatch,
		func(team store.Team, sec store.Secret) (string, error) {
			sealed, rerr := s.resealStoredSecret(team, sec)
			if rerr != nil {
				s.logger.Error("secret reseal: left as stored", "team", team, "name", sec.Name, "pipeline", sec.Pipeline, "err", rerr)
			}
			return sealed, rerr
		})
	if err != nil {
		return counts, fmt.Errorf("reseal stored secrets: %w", err)
	}
	s.logger.Info("stored secrets resealed",
		"resealed", counts.Resealed, "skipped", counts.Skipped, "raced", counts.Raced)
	return counts, nil
}

func (s *Server) probeSecretsKey(ctx context.Context) error {
	sample, err := s.store.SampleSealedSecrets(ctx, "enc:", secretKeyProbeSample)
	if err != nil {
		return fmt.Errorf("sample stored secrets: %w", err)
	}
	tried := 0
	for _, row := range sample {
		binding := bindingForRow(row.Team, &row.Secret)
		var oerr error
		switch {
		case secrets.IsBound(row.Secret.Value):
			_, oerr = openSecret(s.secretsCipher, binding, row.Secret.Value)
		case row.Team == store.DefaultTeam:
			_, oerr = openLegacySecret(s.secretsCipher, binding, row.Secret.Value)
		default:
			continue
		}
		if oerr == nil {
			return nil
		}
		tried++
	}
	// safety: an envelope the probe cannot judge proves nothing about the key,
	// and the reseal seals plaintext rows under it, so a table of envelopes
	// none of which judges the key is refused rather than trusted.
	if tried == 0 && len(sample) > 0 {
		return fmt.Errorf("none of the %d stored secrets sampled is sealed in a way that can confirm the secrets key: "+
			"each is a pre-team envelope outside the default team, which the reseal cannot open; delete or rewrite "+
			"those rows, then start again", len(sample))
	}
	if tried == 0 {
		return nil
	}
	return fmt.Errorf("the secrets key opens none of the %d stored secrets sampled, so it is not the key "+
		"they were sealed under; start with that key, or set it as SPARKWING_SECRETS_PREVIOUS_KEY "+
		"beside the new one and run `sparkwing secrets rotate`", tried)
}
