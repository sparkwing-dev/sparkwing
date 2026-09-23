package cluster

import (
	"context"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/envredact"
	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
)

// triggerChildEnv is the whole environment a claimed trigger's pipeline binary
// starts with. That binary is the team's own code, so it inherits only the
// runtime a Go program and its tools need and Sparkwing settings that are not
// credentials; the launcher's own credentials stay behind, and the run gets
// its own: the runner token it claims and heartbeats with, and the run's cache
// grant.
func triggerChildEnv(ctx context.Context, base []string, opts TriggerLoopOptions, cacheGrant string) []string {
	out := make([]string, 0, len(base)+6)
	for _, item := range base {
		name, value, ok := strings.Cut(item, "=")
		if !ok || triggerChildSets[name] || !triggerChildInherits(name) {
			continue
		}
		if envredact.CredentialName(name) || envredact.CredentialValue(value) || envredact.RedactValue(value) != value {
			continue
		}
		out = append(out, item)
	}
	out = append(out,
		"SPARKWING_CONTROLLER_URL="+opts.ControllerURL,
		"SPARKWING_LOGS_URL="+opts.LogsURL,
		"SPARKWING_AGENT_TOKEN="+opts.Token,
		"SPARKWING_RUNNER_TYPE=kubernetes",
	)
	// safety: the child hands this to the node executors it starts, and an empty
	// value is what sends them to fetch the source directly, as this loop does.
	if opts.GitcacheURL != "" {
		out = append(out, "SPARKWING_GITCACHE_URL="+opts.GitcacheURL)
	}
	if cacheGrant != "" {
		out = append(out, authwire.CacheGrantEnv+"="+cacheGrant)
	}
	if tp := otelutil.TraceParentEnv(ctx); tp != "" {
		out = append(out, tp)
	}
	return out
}

// triggerChildSets are the names triggerChildEnv writes itself, so a stale
// launcher value never shadows the run's own.
var triggerChildSets = map[string]bool{
	"SPARKWING_CONTROLLER_URL": true,
	"SPARKWING_LOGS_URL":       true,
	"SPARKWING_AGENT_TOKEN":    true,
	"SPARKWING_RUNNER_TYPE":    true,
	"SPARKWING_GITCACHE_URL":   true,
	authwire.CacheGrantEnv:     true,
	authwire.CacheTokenEnv:     true,
	"TRACEPARENT":              true,
}

var triggerChildRuntimeEnv = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "SHELL": true,
	"TMPDIR": true, "TMP": true, "TEMP": true, "TZ": true, "LANG": true, "TERM": true,
	"HOSTNAME": true, "POD_NAME": true, "SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
	"KUBERNETES_SERVICE_HOST": true, "KUBERNETES_SERVICE_PORT": true,
	"HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true,
	"http_proxy": true, "https_proxy": true, "no_proxy": true,
	"GOCACHE": true, "GOMODCACHE": true, "GOPATH": true, "GOROOT": true, "GOFLAGS": true,
	"GOPROXY": true, "GOPRIVATE": true, "GONOPROXY": true, "GONOSUMDB": true, "GOSUMDB": true,
	"GOTOOLCHAIN": true, "CGO_ENABLED": true, "DOCKER_HOST": true,
	"AWS_REGION": true, "AWS_DEFAULT_REGION": true,
}

var triggerChildInheritPrefixes = []string{"SPARKWING_", "OTEL_", "LC_"}

func triggerChildInherits(name string) bool {
	if triggerChildRuntimeEnv[name] {
		return true
	}
	for _, prefix := range triggerChildInheritPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
