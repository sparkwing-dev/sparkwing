package runners

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func TestMaskProxyCredentialsCoversEveryFormOfTheCredential(t *testing.T) {
	for _, tc := range []struct {
		name, consumer, value string
		logged                []string
		secrets               []string
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
			name:     "a comma in the password",
			consumer: "npm_config_registry",
			value:    "https://reader:npm-prefix,npm-suffix@cache.invalid/npm",
			logged:   []string{"https://reader:npm-prefix,npm-suffix@cache.invalid/npm", "reader:npm-prefix,npm-suffix", "npm-prefix,npm-suffix"},
			secrets:  []string{"npm-prefix", "npm-suffix"},
		},
		{
			name:     "a pipe in the password",
			consumer: "PIP_INDEX_URL",
			value:    "https://reader:npm-prefix|npm-suffix@cache.invalid/npm",
			logged:   []string{"npm-prefix|npm-suffix", "reader:npm-prefix|npm-suffix"},
			secrets:  []string{"npm-prefix", "npm-suffix"},
		},
		{
			name:     "an @ and a comma in the password",
			consumer: "npm_config_registry",
			value:    "https://reader:safe@private-tail,comma-suffix@cache.invalid/npm",
			logged:   []string{"safe@private-tail,comma-suffix", "reader:safe@private-tail,comma-suffix"},
			secrets:  []string{"private-tail", "comma-suffix"},
		},
		{
			name:     "an @ and a pipe in the password",
			consumer: "PIP_EXTRA_INDEX_URL",
			value:    "https://reader:safe@private-tail|pipe-suffix@cache.invalid/npm",
			logged:   []string{"safe@private-tail|pipe-suffix", "reader:safe@private-tail|pipe-suffix"},
			secrets:  []string{"private-tail", "pipe-suffix"},
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
			consumer := tc.consumer
			if consumer == "" {
				consumer = "GOPROXY"
			}
			if _, err := procenv.Build(procenv.Install, procenv.Base{Ambient: []string{consumer + "=" + tc.value}, SubmitEnvAllow: []string{consumer}, Masker: m}, procenv.Runtime{}); err != nil {
				t.Fatal(err)
			}
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

func TestMaskProxyCredentialsLeavesValuesWithoutACredential(t *testing.T) {
	m := secrets.NewMasker()
	if _, err := procenv.Build(procenv.Install, procenv.Base{Local: true, Captured: []string{"GOPROXY=https://proxy.golang.org,direct", "HTTPS_PROXY=https://proxy-user:pw@one.invalid"}, Masker: m}, procenv.Runtime{}); err != nil {
		t.Fatal(err)
	}
	if got := m.Mask("fell back to direct via https://proxy.golang.org"); got != "fell back to direct via https://proxy.golang.org" {
		t.Fatalf("masked %q", got)
	}
}
