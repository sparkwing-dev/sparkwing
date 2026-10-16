package userconfig

// Config owns the complete machine file so every reader rejects unknown keys.
// Pointer, map and slice absence is unset; explicit empty values remain set.
// Null, duplicate keys and additional YAML documents are invalid.
type Config struct {
	Profiles  map[string]Profile `yaml:"profiles,omitempty" json:"profiles,omitempty"`
	Repos     *ReposConfig       `yaml:"repos,omitempty" json:"repos,omitempty"`
	Admission *AdmissionConfig   `yaml:"admission,omitempty" json:"admission,omitempty"`
	Runner    *RunnerConfig      `yaml:"runner,omitempty" json:"runner,omitempty"`
	Cache     *CacheConfig       `yaml:"cache,omitempty" json:"cache,omitempty"`
	Machine   *MachineConfig     `yaml:"machine,omitempty" json:"machine,omitempty"`
	Debug     *DebugConfig       `yaml:"debug,omitempty" json:"debug,omitempty"`
	Run       *RunConfig         `yaml:"run,omitempty" json:"run,omitempty"`
}

// Profile is only a named controller connection; storage uses that controller.
type Profile struct {
	Controller Connection `yaml:"controller" json:"controller"`
}

// Connection keeps the user's credential in an owner-only file, never env.
// It cannot be serialized in errors, diagnostics, run specs or process arguments.
type Connection struct {
	URL   string  `yaml:"url" json:"url"`
	Token *string `yaml:"token,omitempty" json:"-"`
}

// ReposConfig is the only fallback source for resolving a local child checkout.
type ReposConfig struct {
	Repos            []RepoPath `yaml:"repos,omitempty" json:"repos,omitempty"`
	FallbackPaths    []string   `yaml:"fallback_paths,omitempty" json:"fallback_paths,omitempty"`
	AutoRegister     *bool      `yaml:"auto_register,omitempty" json:"auto_register,omitempty"`
	IncludeWorktrees *bool      `yaml:"include_worktrees,omitempty" json:"include_worktrees,omitempty"`
}

// RepoPath cannot grant controller authority merely by naming a checkout.
type RepoPath struct {
	Path string `yaml:"path" json:"path"`
}

// AdmissionConfig preserves machine admission independently of placement.
type AdmissionConfig struct {
	Budget *string          `yaml:"budget,omitempty" json:"budget,omitempty"`
	Mode   *string          `yaml:"mode,omitempty" json:"mode,omitempty"`
	Custom *AdmissionCustom `yaml:"custom,omitempty" json:"custom,omitempty"`
	Jev    *AdmissionJev    `yaml:"jev,omitempty" json:"jev,omitempty"`
}

// AdmissionCustom records explicit zero separately from the policy defaults.
type AdmissionCustom struct {
	BackfillDelay    map[string]string `yaml:"backfill_delay,omitempty" json:"backfill_delay,omitempty"`
	ClassWeight      map[string]int    `yaml:"class_weight,omitempty" json:"class_weight,omitempty"`
	AgingEvery       *uint64           `yaml:"aging_every,omitempty" json:"aging_every,omitempty"`
	InteractiveBurst *AdmissionBurst   `yaml:"interactive_burst,omitempty" json:"interactive_burst,omitempty"`
}

// AdmissionBurst retains the existing admission policy's measured burst bound.
type AdmissionBurst struct {
	Cores      *float64 `yaml:"cores,omitempty" json:"cores,omitempty"`
	MaxP99     *string  `yaml:"max_p99,omitempty" json:"max_p99,omitempty"`
	MinSamples *int     `yaml:"min_samples,omitempty" json:"min_samples,omitempty"`
}

// AdmissionJev names the existing admission predictor and its failure bounds.
type AdmissionJev struct {
	Model          *string  `yaml:"model,omitempty" json:"model,omitempty"`
	Endpoint       *string  `yaml:"endpoint,omitempty" json:"endpoint,omitempty"`
	Timeout        *string  `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	MinConfidence  *float64 `yaml:"min_confidence,omitempty" json:"min_confidence,omitempty"`
	MinProbability *float64 `yaml:"min_probability,omitempty" json:"min_probability,omitempty"`
	MaxBackfill    *string  `yaml:"max_backfill,omitempty" json:"max_backfill,omitempty"`
}

// RunnerConfig supplies machine claim preferences; pools and billing facts come
// from its root capability. Repos fences use of the owner's git credentials.
type RunnerConfig struct {
	Controller        *string  `yaml:"controller,omitempty" json:"controller,omitempty"`
	CredentialsDir    *string  `yaml:"credentials_dir,omitempty" json:"credentials_dir,omitempty"`
	Repos             []string `yaml:"repos,omitempty" json:"repos,omitempty"`
	AcceptWorkingTree *bool    `yaml:"accept_working_tree,omitempty" json:"accept_working_tree,omitempty"`
	MaxConcurrent     *int     `yaml:"max_concurrent,omitempty" json:"max_concurrent,omitempty"`
	Labels            []string `yaml:"labels,omitempty" json:"labels,omitempty"`
	Contribution      *string  `yaml:"contribution,omitempty" json:"contribution,omitempty"`
	SpawnPolicy       *string  `yaml:"spawn_policy,omitempty" json:"spawn_policy,omitempty"`
	HolderPrefix      *string  `yaml:"holder_prefix,omitempty" json:"holder_prefix,omitempty"`
	Poll              *string  `yaml:"poll,omitempty" json:"poll,omitempty"`
	Lease             *string  `yaml:"lease,omitempty" json:"lease,omitempty"`
	Heartbeat         *string  `yaml:"heartbeat,omitempty" json:"heartbeat,omitempty"`
	LocalAdmission    *bool    `yaml:"local_admission,omitempty" json:"local_admission,omitempty"`
	KeepFree          *string  `yaml:"keep_free,omitempty" json:"keep_free,omitempty"`
}

// CacheConfig bounds only the local binary cache; explicit zero disables a bound.
type CacheConfig struct {
	MaxBytes   *string `yaml:"max_bytes,omitempty" json:"max_bytes,omitempty"`
	MaxEntries *int    `yaml:"max_entries,omitempty" json:"max_entries,omitempty"`
}

// MachineConfig preserves an explicit box identity when the hostname changes.
type MachineConfig struct {
	BoxID *string `yaml:"box_id,omitempty" json:"box_id,omitempty"`
}

// DebugConfig bounds pauses without putting debug controls into child env.
type DebugConfig struct {
	PauseTimeout *string `yaml:"pause_timeout,omitempty" json:"pause_timeout,omitempty"`
}

// RunConfig grants no authority: allowed env still passes procenv filtering, and
// cache reads still require the user's repo-bound data.read capability.
type RunConfig struct {
	SubmitEnvAllow []string `yaml:"submit_env_allow,omitempty" json:"submit_env_allow,omitempty"`
	CacheFrom      *string  `yaml:"cache_from,omitempty" json:"cache_from,omitempty"`
}
