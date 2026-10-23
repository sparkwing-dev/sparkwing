package backends

import "os"

// Spec declares a secret source consumed by internal/secretsource.
// Type selects which fields the resolver requires.
type Spec struct {
	Type   string `yaml:"type"`
	Prefix string `yaml:"prefix,omitempty"`
	Path   string `yaml:"path,omitempty"`
	URL    string `yaml:"url,omitempty"`
	Token  string `yaml:"token,omitempty"`

	// TokenEnv lets checked-in config name an environment variable instead
	// of carrying a token. The secret resolver reads it at use time.
	TokenEnv string `yaml:"token_env,omitempty"`
}

// Secret source type discriminators.
const (
	TypeFilesystem = "filesystem"
	TypeController = "controller"
	TypeEnv        = "env"
	TypeNone       = "none"
)

// MigrationGuideURL explains removed profile configuration.
const MigrationGuideURL = "https://sparkwing.dev/docs/migration-guide/"

// ResolvedToken returns Token when set, otherwise the value of TokenEnv.
// Callers that require authentication must reject an empty result.
func (s *Spec) ResolvedToken() string {
	if s == nil {
		return ""
	}
	if s.Token != "" {
		return s.Token
	}
	return os.Getenv(s.TokenEnv)
}
