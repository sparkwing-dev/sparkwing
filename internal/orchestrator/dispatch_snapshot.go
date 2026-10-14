package orchestrator

import (
	"strings"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

var envAllowPrefixes = []string{
	"SPARKWING_",
	"GITHUB_",
}

// safety: the AWS SDK reads the S3 endpoint from its own names, so a run
// against MinIO or R2 would reach AWS without them; XDG_CONFIG_HOME locates
// config.yaml, without which a child reads another file's settings.
var envAllowExact = map[string]bool{
	"KUBERNETES_SERVICE_HOST": true,
	"PATH":                    true,
	"HOME":                    true,
	"HOSTNAME":                true,
	"AWS_ENDPOINT_URL_S3":     true,
	"AWS_ENDPOINT_URL":        true,
	"XDG_CONFIG_HOME":         true,
}

// safety: a store path and its reason name where one particular parent put its
// runs, so a captured shell value would send an unrelated run into that file.
// A parent that means to pass them appends them after this filter.
var envDenyExact = map[string]bool{
	wingdclient.HostBinEnv:               true,
	StandaloneStateDBEnv:                 true,
	StandaloneReasonEnv:                  true,
	"SPARKWING_AGENT_TOKEN":              true,
	"SPARKWING_CONTROLLER_URL":           true,
	"SPARKWING_ONLY":                     true,
	"SPARKWING_NODE_CLAIM_HOLDER":        true,
	"SPARKWING_NODE_CLAIM_LEASE_SECONDS": true,
	"SPARKWING_NODE_CLAIM_GENERATION":    true,
	"SPARKWING_NODE_CLAIM_MEMBERSHIP":    true,
	"SPARKWING_NODE_CLAIM_RESERVATION":   true,
	"SPARKWING_FLEET_PARENT_GUARD":       true,
	"SPARKWING_FLEET_PARENT_TOKEN":       true,
}

func envAllowed(name string) bool {
	if envDenyExact[name] {
		return false
	}
	for _, p := range envAllowPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return envAllowExact[name]
}
