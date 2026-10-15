package projectconfig

// Config excludes connection credentials because a checkout cannot choose where
// a user's secrets are sent. Nil optional fields mean unset; explicit null fails.
type Config struct {
	Defaults  *Defaults  `yaml:"defaults,omitempty" json:"defaults,omitempty"`
	Pipelines []Pipeline `yaml:"pipelines,omitempty" json:"pipelines,omitempty"`
	Sparks    []Spark    `yaml:"sparks,omitempty" json:"sparks,omitempty"`
}

// Defaults applies even when a pipeline has no entry in the project file.
type Defaults struct {
	Args     map[string]string `yaml:"args,omitempty" json:"args,omitempty"`
	Guards   *Guards           `yaml:"guards,omitempty" json:"guards,omitempty"`
	Requires []string          `yaml:"requires,omitempty" json:"requires,omitempty"`
	Prefers  []string          `yaml:"prefers,omitempty" json:"prefers,omitempty"`
}

// Pipeline keeps author declarations separate from the host's sealed run spec.
type Pipeline struct {
	Name        string            `yaml:"name" json:"name"`
	Entrypoint  string            `yaml:"entrypoint" json:"entrypoint"`
	Description *string           `yaml:"description,omitempty" json:"description,omitempty"`
	Hidden      *bool             `yaml:"hidden,omitempty" json:"hidden,omitempty"`
	Source      *string           `yaml:"source,omitempty" json:"source,omitempty"`
	Profile     *string           `yaml:"profile,omitempty" json:"profile,omitempty"`
	Args        map[string]string `yaml:"args,omitempty" json:"args,omitempty"`
	Guards      *Guards           `yaml:"guards,omitempty" json:"guards,omitempty"`
	Requires    []string          `yaml:"requires,omitempty" json:"requires,omitempty"`
	Prefers     []string          `yaml:"prefers,omitempty" json:"prefers,omitempty"`
	On          *Events           `yaml:"on,omitempty" json:"on,omitempty"`
}

// Guards replaces the complete lower-priority block, including when empty.
// Tokens are where:local|controller, arg:key=value, and git:branch=name|default.
type Guards struct {
	When   []string `yaml:"when,omitempty" json:"when,omitempty"`
	Unless []string `yaml:"unless,omitempty" json:"unless,omitempty"`
}

// Events contains only events with an intake implementation.
type Events struct {
	Push        *PushEvent        `yaml:"push,omitempty" json:"push,omitempty"`
	PullRequest *PullRequestEvent `yaml:"pull_request,omitempty" json:"pull_request,omitempty"`
	Schedule    []Schedule        `yaml:"schedule,omitempty" json:"schedule,omitempty"`
	PreCommit   *Hook             `yaml:"pre_commit,omitempty" json:"pre_commit,omitempty"`
	PrePush     *Hook             `yaml:"pre_push,omitempty" json:"pre_push,omitempty"`
	PostCommit  *Hook             `yaml:"post_commit,omitempty" json:"post_commit,omitempty"`
}

// PushEvent filters an authenticated push; it cannot establish repository trust.
type PushEvent struct {
	Branches []string `yaml:"branches,omitempty" json:"branches,omitempty"`
	Paths    []string `yaml:"paths,omitempty" json:"paths,omitempty"`
}

// PullRequestEvent filters intake without changing the fork-PR refusal.
type PullRequestEvent struct {
	Actions  []string `yaml:"actions,omitempty" json:"actions,omitempty"`
	Branches []string `yaml:"branches,omitempty" json:"branches,omitempty"`
}

// Schedule identifies a durable schedule separately from any run it starts.
// Where is local or controller; catch_up is a duration, never clamped.
type Schedule struct {
	Name    *string           `yaml:"name,omitempty" json:"name,omitempty"`
	Cron    string            `yaml:"cron" json:"cron"`
	Where   string            `yaml:"where" json:"where"`
	TZ      *string           `yaml:"tz,omitempty" json:"tz,omitempty"`
	Overlap *string           `yaml:"overlap,omitempty" json:"overlap,omitempty"`
	CatchUp *string           `yaml:"catch_up,omitempty" json:"catch_up,omitempty"`
	Args    map[string]string `yaml:"args,omitempty" json:"args,omitempty"`
}

// Hook requires an empty mapping so misspelled hook settings cannot be ignored.
type Hook struct{}

// Spark pins a library independently of the controller connection.
type Spark struct {
	Name    string `yaml:"name" json:"name"`
	Source  string `yaml:"source" json:"source"`
	Version string `yaml:"version" json:"version"`
}

// Parse refuses unknown keys, duplicate keys, null values, multiple documents,
// malformed enums and duplicate pipeline or schedule names. Errors name the
// key and line without echoing values. Empty lists and maps remain explicit.
func Parse(raw []byte) (*Config, error) { return parse(raw) }

// Load preserves absence as nil; invalid or unreadable files fail with their path.
func Load(path string) (*Config, error) { return load(path) }

// Discover searches from a checkout for its project file without reading machine
// connections or resolving run settings before the checkout has been acquired.
func Discover(startDir string) (path string, cfg *Config, err error) {
	return discover(startDir)
}

// Validate is shared by file parsing and server-side seal-point validation.
// Profiles is an unknown project key; there is no fallback to project credentials.
func Validate(cfg Config) error { return validateConfig(cfg) }
