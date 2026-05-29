package logpretty

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// renderRunStart emits a run_start record (with the given profile +
// backends attrs) and flushes, returning the rendered setup block.
func renderRunStart(t *testing.T, prof, backends map[string]any) string {
	t.Helper()
	var buf bytes.Buffer
	r := NewPrettyRendererTo(&buf, false)
	attrs := map[string]any{"run_id": "run-1", "pipeline": "demo"}
	if prof != nil {
		attrs["profile"] = prof
	}
	if backends != nil {
		attrs["backends"] = backends
	}
	r.Emit(sparkwing.LogRecord{TS: time.Now(), Level: "info", Event: "run_start", Attrs: attrs})
	r.Flush()
	return buf.String()
}

func TestProfileBanner_OmittedWithoutProfileAttrs(t *testing.T) {
	out := renderRunStart(t, nil, nil)
	if strings.Contains(out, "profile:") {
		t.Errorf("no profile banner expected without profile attrs; got:\n%s", out)
	}
}

func TestProfileBanner_ControllerRendersAllFields(t *testing.T) {
	out := renderRunStart(t,
		map[string]any{"name": "prod", "source": "flag", "detect_via": "", "mirror_local": true},
		map[string]any{"state": "controller://prod", "logs": "controller://prod", "cache": "controller://prod"},
	)
	// Order: profile header, then via/state/logs/cache/mirror.
	for _, want := range []string{
		"profile:  prod",
		"via:    --profile flag",
		"state:  controller://prod",
		"logs:   controller://prod",
		"cache:  controller://prod",
		"mirror: on",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("banner missing %q; got:\n%s", want, out)
		}
	}
	// Token / controller URL must never leak (we only emit the strings
	// we were given, but assert defensively).
	if strings.Contains(out, "swu_") {
		t.Errorf("token leaked:\n%s", out)
	}
}

func TestProfileBanner_MirrorOmittedForLocalProfile(t *testing.T) {
	out := renderRunStart(t,
		map[string]any{"name": "laptop", "source": "builtin", "detect_via": "", "mirror_local": true},
		map[string]any{"state": "sqlite", "logs": "filesystem:~/.cache/sparkwing/logs", "cache": "filesystem:~/.cache/sparkwing"},
	)
	if strings.Contains(out, "mirror:") {
		t.Errorf("mirror line should be omitted for a local (sqlite) profile; got:\n%s", out)
	}
	if !strings.Contains(out, "via:    built-in fallback") {
		t.Errorf("builtin via phrase missing; got:\n%s", out)
	}
}

func TestProfileBanner_MirrorOffWhenDisabled(t *testing.T) {
	out := renderRunStart(t,
		map[string]any{"name": "ci", "source": "flag", "detect_via": "", "mirror_local": false},
		map[string]any{"state": "s3://ci/state", "logs": "-", "cache": "-"},
	)
	if !strings.Contains(out, "mirror: off") {
		t.Errorf("mirror should read off when disabled on a non-local profile; got:\n%s", out)
	}
}

func TestProfileBanner_ViaPhrasePerSource(t *testing.T) {
	cases := []struct {
		source, want string
	}{
		{"flag", "via:    --profile flag"},
		{"project", "via:    project hint (.sparkwing/sparkwing.yaml profile:)"},
		{"builtin", "via:    built-in fallback"},
	}
	for _, tc := range cases {
		out := renderRunStart(t,
			map[string]any{"name": "x", "source": tc.source, "mirror_local": true},
			map[string]any{"state": "s3://x/state", "logs": "-", "cache": "-"},
		)
		if !strings.Contains(out, tc.want) {
			t.Errorf("source %q: banner missing %q; got:\n%s", tc.source, tc.want, out)
		}
	}
}
