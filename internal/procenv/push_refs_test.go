package procenv

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/gitenv"
)

func TestHookPushRefsFollowCapturedLocalEnvironment(t *testing.T) {
	const refs = "refs/heads/topic abc refs/heads/main def\nrefs/tags/v1 abc refs/tags/v1 def"
	for _, kind := range []Kind{Plan, Attempt, Eval} {
		base := Base{Local: true, Captured: []string{gitenv.PushRefsVar + "=" + refs}, Ambient: []string{gitenv.PushRefsVar + "=daemon"}}
		env, err := buildEntries(kind, base, Runtime{})
		if err != nil {
			t.Fatal(err)
		}
		if got := envMap(env)[gitenv.PushRefsVar]; got != refs {
			t.Fatalf("kind %d push refs = %q, want captured %q", kind, got, refs)
		}
	}
}
