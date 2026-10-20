// Package backends defines the typed backend specs (secrets, state) a
// storage profile resolves to. The specs are consumed by the storeurl
// factories ([internal/storage/storeurl]) to open concrete stores and by
// [internal/profile] to describe where a profile's runs persist.
package backends

import (
	"fmt"
	"os"
)

// Surfaces groups the persistence destinations a profile declares.
// A nil pointer means "not declared at this layer."
type Surfaces struct {
	Secrets *Spec `yaml:"secrets,omitempty"`
	State   *Spec `yaml:"state,omitempty"`
}

// Spec is one backend declaration. Type is the discriminator; the
// remaining fields are interpreted per-type by the storeurl factories
// (state) and the secrets resolver (secrets).
type Spec struct {
	Type string `yaml:"type"`

	// Bucket is read by nothing; it lets a profile that still names an S3
	// state bucket decode far enough to get the removal refusal.
	Bucket string `yaml:"bucket,omitempty"`
	Prefix string `yaml:"prefix,omitempty"`
	Path   string `yaml:"path,omitempty"`
	URL    string `yaml:"url,omitempty"`
	Token  string `yaml:"token,omitempty"`

	// TokenEnv names an env var the runtime reads to populate Token
	// at use time. Set TokenEnv on checked-in config so the token
	// itself stays out of git; leave Token empty when TokenEnv is
	// set. The env-var read happens lazily at the call site that
	// needs the token (secrets resolver, controller storeurl).
	TokenEnv string `yaml:"token_env,omitempty"`

	// Controller names a profile from config.yaml for type=controller
	// backends. The orchestrator resolves the name to a controller URL
	// and bearer token via the same profile-lookup callback used by
	// profile secret sources.
	Controller string `yaml:"controller,omitempty"`
}

// Backend type discriminators.
const (
	TypeFilesystem = "filesystem"
	TypeS3         = "s3"
	TypeController = "controller"
	TypeSQLite     = "sqlite"
	TypeEnv        = "env"  // env vars; valid only on the secrets surface
	TypeNone       = "none" // explicit "no backend"; valid only on the secrets surface
)

// Validate enforces that a Surfaces bundle declares both surfaces
// (secrets, state), with each surface's own invariants. The name is woven
// into the error so the operator can tell which profile is invalid.
func (s Surfaces) Validate(name string) error {
	if s.Secrets == nil {
		return fmt.Errorf("profile %q: secrets surface is required (use type=none for pipelines that never call Secret())", name)
	}
	if err := s.Secrets.ValidateSecrets(); err != nil {
		return fmt.Errorf("profile %q: %w", name, err)
	}
	if s.State == nil {
		return fmt.Errorf("profile %q: state surface is required: a project profile declares state: {type: sqlite} "+
			"for this machine or state: {type: controller} for its controller; or remove the profile", name)
	}
	if s.State.Type == TypeNone || s.State.Type == TypeEnv {
		return fmt.Errorf("profile %q: state cannot be type=%s", name, s.State.Type)
	}
	if err := s.State.ValidateFields("state"); err != nil {
		return fmt.Errorf("profile %q: %w", name, err)
	}
	return nil
}

// ValidateFields enforces the fields a backend type cannot work
// without, so a spec missing one fails when its config loads rather than
// when a command first opens it.
//
// A type whose missing field has a real default is deliberately absent:
// sqlite without a path is filled in with the host's own state database
// by the profile resolver, so demanding one here would reject configs
// that work.
func (s *Spec) ValidateFields(surface string) error {
	if s == nil {
		return nil
	}
	if err := s.refuseRemovedState(surface); err != nil {
		return err
	}
	switch s.Type {
	case "":
		return fmt.Errorf("%s backend: type is required", surface)
	case TypeFilesystem:
		if s.Path == "" {
			return fmt.Errorf("%s backend type=filesystem requires path", surface)
		}
	case TypeController:
		if s.Controller == "" && s.URL == "" {
			return fmt.Errorf("%s backend type=controller requires controller: <profile-name>", surface)
		}
	}
	return nil
}

// MigrationGuideURL is where the removal of S3 and Postgres state is
// explained, under "S3 and Postgres state profiles are removed".
const MigrationGuideURL = "https://sparkwing.dev/docs/migration-guide/"

// safety: a removed state type that loaded would leave the profile's runs
// recording nowhere it names, so the config fails to load instead.
func (s *Spec) refuseRemovedState(surface string) error {
	if surface != "state" || (s.Type != TypeS3 && s.Type != "postgres") {
		return nil
	}
	return fmt.Errorf("state.type: %s is removed: S3 and Postgres state profiles are gone and sparkwing run records to this machine; "+
		"in config.yaml delete the profile's state key or the profile, in .sparkwing/sparkwing.yaml set state to sqlite or controller or remove the profile, "+
		"and run on a controller with sparkwing pipeline trigger <pipeline> --profile <name> "+
		"(see \"S3 and Postgres state profiles are removed\" in %s)", s.Type, MigrationGuideURL)
}

// ResolvedToken returns Token directly when it's set, else reads
// TokenEnv from the process environment. Empty string when neither is
// configured -- callers that need auth should error explicitly on
// empty rather than relying on ResolvedToken to flag it.
func (s *Spec) ResolvedToken() string {
	if s == nil {
		return ""
	}
	if s.Token != "" {
		return s.Token
	}
	if s.TokenEnv != "" {
		return envOr(s.TokenEnv, "")
	}
	return ""
}

func envOr(name, fallback string) string {
	if v, ok := lookupEnv(name); ok {
		return v
	}
	return fallback
}

var lookupEnv = os.LookupEnv

// ValidateSecrets enforces the type vocabulary for the secrets
// surface: only controller / filesystem / env make sense (state
// backends like sqlite are nonsensical for secret lookup). Returns
// nil for an absent spec.
func (s *Spec) ValidateSecrets() error {
	if s == nil {
		return nil
	}
	switch s.Type {
	case TypeController:
		if s.URL == "" {
			return secretsErr("type=controller requires url")
		}
	case TypeFilesystem:
		if s.Path == "" {
			return secretsErr("type=filesystem requires path")
		}
	case TypeEnv:
	case TypeNone:
	case "":
		return secretsErr("type is required (controller | filesystem | env | none)")
	default:
		return secretsErr("unsupported type %q (controller | filesystem | env | none)", s.Type)
	}
	return nil
}

func secretsErr(format string, a ...any) error {
	return fmt.Errorf("secrets backend: "+format, a...)
}
