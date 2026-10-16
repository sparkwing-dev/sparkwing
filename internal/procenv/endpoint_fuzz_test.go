package procenv

import (
	"encoding/hex"
	"net/url"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func FuzzConsumerCredentialsMasked(f *testing.F) {
	f.Add([]byte("private-password"), []byte("/@scope?next=@other"))
	f.Fuzz(func(t *testing.T, secret, suffix []byte) {
		if len(secret) == 0 || len(secret) > 128 || len(suffix) > 256 {
			t.Skip()
		}
		password := hex.EncodeToString(secret)
		endpoint := "https://" + url.UserPassword("alice", password).String() + "@proxy.example/@scope/" + url.PathEscape(string(suffix))
		for name, value := range map[string]string{
			"HTTPS_PROXY":         endpoint,
			"GOPROXY":             "https://public.example," + endpoint + "|direct",
			"PIP_EXTRA_INDEX_URL": "https://public.example " + endpoint,
			"npm_config_registry": endpoint,
		} {
			masker := secrets.NewMasker()
			_, err := Build(Install, Base{Local: true, Captured: []string{name + "=" + value}, Masker: masker}, Runtime{})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(masker.Mask(password), password) {
				t.Fatalf("consumer password remains visible for %s", name)
			}
		}
	})
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
