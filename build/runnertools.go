// Package build holds the image recipes and the runner image's declared
// toolset, which the controller schedules Sparkwing Cloud work against.
package build

import _ "embed"

// RunnerTools is build/runner-tools: one tool per line, # starts a comment.
//
//go:embed runner-tools
var RunnerTools string
