package orchestrator

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

// safety: a proxy setting's credential is masked however a node logs it: the
// whole value, one member, the userinfo or the password, raw or decoded.
func TestMaskProxyCredentialsCoversEveryFormOfTheCredential(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		logged      []string
		secrets     []string
	}{
		{
			name:    "an @ in the password",
			value:   "https://reader:prefix@private-tail@cache.invalid/golang",
			logged:  []string{"https://reader:prefix@private-tail@cache.invalid/golang", "reader:prefix@private-tail", "prefix@private-tail"},
			secrets: []string{"private-tail", "prefix@private"},
		},
		{
			name:    "a scheme-less member",
			value:   "reader:schemeless-pass@cache.invalid/golang,direct",
			logged:  []string{"reader:schemeless-pass@cache.invalid/golang", "schemeless-pass"},
			secrets: []string{"schemeless-pass"},
		},
		{
			name:    "a comma in the password",
			value:   "https://reader:npm-prefix,npm-suffix@cache.invalid/npm",
			logged:  []string{"https://reader:npm-prefix,npm-suffix@cache.invalid/npm", "https://reader:npm-prefix", "npm-suffix@cache.invalid/npm", "npm-prefix,npm-suffix"},
			secrets: []string{"npm-prefix", "npm-suffix"},
		},
		{
			name:    "a pipe in the password",
			value:   "https://reader:npm-prefix|npm-suffix@cache.invalid/npm",
			logged:  []string{"npm-prefix|npm-suffix", "reader:npm-prefix|npm-suffix"},
			secrets: []string{"npm-prefix", "npm-suffix"},
		},
		{
			name:    "an @ and a comma in the password",
			value:   "https://reader:safe@private-tail,comma-suffix@cache.invalid/npm",
			logged:  []string{"safe@private-tail,comma-suffix", "reader:safe@private-tail,comma-suffix"},
			secrets: []string{"private-tail", "comma-suffix"},
		},
		{
			name:    "an @ and a pipe in the password",
			value:   "https://reader:safe@private-tail|pipe-suffix@cache.invalid/npm",
			logged:  []string{"safe@private-tail|pipe-suffix", "reader:safe@private-tail|pipe-suffix"},
			secrets: []string{"private-tail", "pipe-suffix"},
		},
		{
			name:    "an encoded password",
			value:   "https://reader:s%61fe-pip@cache.invalid/simple",
			logged:  []string{"s%61fe-pip", "safe-pip", "reader:safe-pip"},
			secrets: []string{"s%61fe-pip", "safe-pip"},
		},
		{
			name:    "members logged one at a time",
			value:   "https://a:go-one@one.invalid|https://b:go-two@two.invalid,direct",
			logged:  []string{"https://a:go-one@one.invalid", "https://b:go-two@two.invalid"},
			secrets: []string{"go-one", "go-two"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := secrets.NewMasker()
			maskProxyCredentials(m, tc.value)
			for _, line := range tc.logged {
				masked := m.Mask("value " + line + " end")
				for _, secret := range tc.secrets {
					if strings.Contains(masked, secret) {
						t.Errorf("logging %q shows %q: %s", line, secret, masked)
					}
				}
			}
		})
	}
}

// safety: GOPROXY's keywords and a value with no credential stay readable in logs.
func TestMaskProxyCredentialsLeavesValuesWithoutACredential(t *testing.T) {
	m := secrets.NewMasker()
	maskProxyCredentials(m, "https://proxy.golang.org,direct")
	maskProxyCredentials(m, "https://a:pw@one.invalid,direct")
	if got := m.Mask("fell back to direct via https://proxy.golang.org"); got != "fell back to direct via https://proxy.golang.org" {
		t.Fatalf("masked %q", got)
	}
}
