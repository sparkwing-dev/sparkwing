package engine

import "context"

// SDKInstallRequest gives the verified matching engine its acquired source and filtered install base.
// Directory is the checkout, PipelineDir is relative to it, and Environment comes from procenv.Install.
// No capability, source credential, release origin or replacement engine may be supplied here.
type SDKInstallRequest struct {
	Directory   string   `json:"directory"`
	PipelineDir string   `json:"pipeline_dir"`
	SDK         SDKKind  `json:"sdk"`
	Environment []string `json:"-"`
}

// SDKCommand names the locally built Go command or Node/main command without protocol argv.
// Command is executed with Directory as cwd by the same selected engine.
type SDKCommand struct {
	Directory string   `json:"directory"`
	Command   []string `json:"command"`
}

// InstallSDK executes the selected verified Sparkwing engine's hidden __sdk-install command.
// The request uses stdin JSON with Environment excluded; the child inherits that filtered base.
// Stdout contains one SDKCommand JSON object and stderr contains masked diagnostics.
// It rechecks the checkout pin against the resolved identity and never resolves to the ambient CLI.
// Go builds use the local binary cache. TypeScript installs from its native lockfile without a preload.
func (r Resolved) InstallSDK(ctx context.Context, request SDKInstallRequest) (SDKCommand, error) {
	panic("unimplemented: c3-engine")
}
