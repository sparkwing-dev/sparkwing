package procenv

import (
	"encoding/hex"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func FuzzConsumerCredentialsMasked(f *testing.F) {
	f.Add([]byte("private-password"), []byte("/@scope?next=@other"))
	f.Add([]byte("safe@private-tail,comma-suffix|pipe-suffix"), []byte("/%bad?at=@host"))
	f.Fuzz(func(t *testing.T, secret, suffix []byte) {
		if len(secret) == 0 || len(secret) > 128 || len(suffix) > 256 {
			t.Skip()
		}
		for _, name := range []string{"HTTPS_PROXY", "GOPROXY", "GOSUMDB", "PIP_INDEX_URL", "PIP_EXTRA_INDEX_URL", "npm_config_registry"} {
			password := "credential-" + hex.EncodeToString(secret) + "@inner:part,comma|pipe"
			rawPassword := "%63" + password[1:]
			if name == "GOPROXY" {
				rawPassword = strings.NewReplacer(",", "%2c", "|", "%7c").Replace(rawPassword)
			}
			public := "proxy.example/@scope/" + hex.EncodeToString(suffix) + "/path://fake-user:fake-pass@public.example?other=@public"
			endpoint := "https://private-user:" + rawPassword + "@" + public
			if secret[0]%2 == 0 {
				endpoint = strings.TrimPrefix(endpoint, "https://")
			}
			value := endpoint
			if name == "GOPROXY" {
				value = " \thttps://public.example , \n" + value + " | direct "
			} else if name == "GOSUMDB" {
				value = "sum.example+key " + value
			} else if name == "PIP_EXTRA_INDEX_URL" {
				value = "https://public.example " + value
			}
			entry := name + "=" + value
			for _, mode := range []string{"captured", "ambient", "startup", "cleanup", "step"} {
				masker := secrets.NewMasker()
				base := Base{Local: true, Masker: masker}
				runtime := Runtime{}
				kind := Install
				switch mode {
				case "captured":
					base.Captured = []string{entry}
				case "ambient":
					base.Local = false
					base.Ambient = []string{entry}
					base.SubmitEnvAllow = []string{name}
				case "startup":
					kind = Attempt
					runtime.Startup = []Assignment{{Name: name, Value: value}}
				case "cleanup", "step":
					kind = Cleanup
					if mode == "step" {
						kind = StepTool
					}
					runtime.AttemptEnv = []string{entry}
				}
				env, err := Build(kind, base, runtime)
				if err != nil || !slices.Contains(env, entry) {
					t.Fatalf("%s/%s changed endpoint: %v", name, mode, err)
				}
				for _, credential := range []string{password, rawPassword, "private-user"} {
					if strings.Contains(masker.Mask(credential), credential) {
						t.Fatalf("%s/%s left credential visible", name, mode)
					}
				}
				if mode != "ambient" && !strings.Contains(masker.Mask(endpoint), public) {
					t.Fatalf("%s/%s masked public endpoint fields", name, mode)
				}
			}
		}
	})
}

func TestGoProxyDelimitersKeepPublicAuthorityVisible(t *testing.T) {
	for _, separator := range []string{",", "|"} {
		value := "reader:safe@private-tail" + separator + "suffix@next.example/@scope?other=@public"
		masker := secrets.NewMasker()
		_, err := Build(Install, Base{Local: true, Captured: []string{"GOPROXY=" + value}, Masker: masker}, Runtime{})
		if err != nil {
			t.Fatal(err)
		}
		if got, want := masker.Mask(value), "***@private-tail"+separator+"***@next.example/@scope?other=@public"; got != want {
			t.Fatalf("masked Go proxy list = %q, want %q", got, want)
		}
	}
}

func TestInvalidEndpointAuthorityDiagnosticsContainOnlyTheName(t *testing.T) {
	for _, value := range []string{
		"https://reader:private-password%2@host.example",
		"https://reader:private-password\n@host.example",
		"https://reader:private-password@%",
	} {
		_, err := Build(Install, Base{Local: true, Captured: []string{"npm_config_registry=" + value}, Masker: secrets.NewMasker()}, Runtime{})
		if err == nil || err.Error() != `invalid proxy URL in "npm_config_registry"` {
			t.Fatalf("invalid endpoint diagnostic = %v", err)
		}
	}
}

func TestEndpointPathCannotSupplyAnAuthority(t *testing.T) {
	for _, endpoint := range []string{
		"proxy.example/path://fake-user:fake-pass@public.example",
		"//proxy.example/path://fake-user:fake-pass@public.example",
		"https://proxy.example/path://fake-user:fake-pass@public.example",
		"proxy.example?next=https://fake-user:fake-pass@public.example",
	} {
		masker := secrets.NewMasker()
		_, err := Build(Install, Base{Local: true, Captured: []string{"npm_config_registry=" + endpoint}, Masker: masker}, Runtime{})
		if err != nil {
			t.Fatal(err)
		}
		if got := masker.Mask(endpoint); got != endpoint {
			t.Fatalf("public endpoint was masked: %q", got)
		}
	}
}

func TestEmptyEndpointCredentialFieldsKeepPublicPunctuation(t *testing.T) {
	for _, tc := range []struct{ userinfo, masked string }{
		{"", ""},
		{":", ":"},
		{":private-password", "***"},
		{"reader:", "***"},
	} {
		endpoint := "https://" + tc.userinfo + "@proxy.example:443"
		masker := secrets.NewMasker()
		_, err := Build(Install, Base{Local: true, Captured: []string{"npm_config_registry=" + endpoint}, Masker: masker}, Runtime{})
		if err != nil {
			t.Fatal(err)
		}
		if got, want := masker.Mask(endpoint), "https://"+tc.masked+"@proxy.example:443"; got != want {
			t.Fatalf("empty credential fields masked public punctuation: %q", got)
		}
	}
}

func FuzzCredentialTransportExcluded(f *testing.F) {
	f.Add([]byte("custom"), []byte("credential-value"))
	f.Fuzz(func(t *testing.T, prefix, value []byte) {
		if len(prefix) > 64 || len(value) > 256 || strings.ContainsRune(string(value), 0) {
			t.Skip()
		}
		name := "CUSTOM_" + hex.EncodeToString(prefix) + "_TOKEN"
		for _, kind := range []Kind{Plan, Attempt, Eval, Install, HostChild} {
			for _, local := range []bool{false, true} {
				entry := name + "=" + string(value)
				env, err := Build(kind, Base{Local: local, Captured: []string{entry}, Ambient: []string{entry}, SubmitEnvAllow: []string{name}}, Runtime{})
				if err != nil {
					t.Fatal(err)
				}
				if _, exists := envMap(env)[name]; exists {
					t.Fatalf("credential inherited at kind %d", kind)
				}
			}
		}
	})
}
