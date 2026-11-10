package procenv

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func TestRemoteToolsKeepHostWindowsProfile(t *testing.T) {
	const host = `C:\private\runner`
	for name, kind := range map[string]Kind{"install": Install, "plan": Plan, "attempt": Attempt, "eval": Eval, "git": Git} {
		t.Run(name, func(t *testing.T) {
			env, err := buildEntries(kind, Base{
				Ambient:  []string{"USERPROFILE=" + host},
				Captured: []string{`USERPROFILE=C:\requester`},
				Masker:   secrets.NewMasker(),
			}, Runtime{})
			if err != nil {
				t.Fatal(err)
			}
			if got := envMap(env)["USERPROFILE"]; got != host {
				t.Fatalf("USERPROFILE = %q, want executing host %q", got, host)
			}
		})
	}
}

func TestSubmittedWindowsProfileCannotSelectHostHome(t *testing.T) {
	if err := CheckSubmitted(map[string][]byte{"USERPROFILE": []byte(`C:\requester`)}, secrets.NewMasker()); err == nil {
		t.Fatal("requester-controlled Windows home was accepted")
	}
}
