package nodeproto

import "github.com/sparkwing-dev/sparkwing/pkg/apierr"

// DescribeDoc is the sole source of secret declarations; plan JSON may never repeat them.
type DescribeDoc struct {
	Pipelines []PipelineDescription `json:"pipelines"`
}

// PipelineDescription supports both SDKs without linking engine registration or YAML code.
type PipelineDescription struct {
	Name        string                   `json:"name"`
	Error       *apierr.Error            `json:"error,omitempty"`
	Short       string                   `json:"short,omitempty"`
	Help        string                   `json:"help,omitempty"`
	Examples    []UsageExample           `json:"examples,omitempty"`
	Args        []ArgumentDescription    `json:"args"`
	EnvVars     []EnvironmentDescription `json:"env_vars,omitempty"`
	Extra       bool                     `json:"extra,omitempty"`
	Risks       []string                 `json:"risks,omitempty"`
	RisksByStep []StepRisks              `json:"risks_by_step,omitempty"`
	Secrets     []SecretDeclaration      `json:"secrets,omitempty"`
}

// SecretDeclaration names required authority without carrying a secret value.
type SecretDeclaration struct {
	Name     string `json:"name"`
	Optional bool   `json:"optional,omitempty"`
}

// ArgumentDescription omits language-specific field names from the shared wire schema.
type ArgumentDescription struct {
	Name        string   `json:"name"`
	Short       string   `json:"short,omitempty"`
	Type        string   `json:"type"`
	Required    bool     `json:"required"`
	Description string   `json:"desc,omitempty"`
	Default     *string  `json:"default,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	Secret      bool     `json:"secret,omitempty"`
	JobID       string   `json:"job_id,omitempty"`
}

// EnvironmentDescription documents user inputs rather than transport or credential variables.
type EnvironmentDescription struct {
	Name        string  `json:"name"`
	Description string  `json:"description,omitempty"`
	Default     *string `json:"default,omitempty"`
}

// UsageExample preserves author help without letting the SDK choose host command semantics.
type UsageExample struct {
	Comment string `json:"comment"`
	Command string `json:"command"`
}

// StepRisks attributes refusal diagnostics to a job and step on every placement.
type StepRisks struct {
	JobID  string   `json:"job_id"`
	StepID string   `json:"step_id"`
	Labels []string `json:"labels"`
}
