package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func FuzzLocalSecretNamesUsePlatformIdentity(f *testing.F) {
	f.Add("SIGNING_MATERIAL", "private-value", true)
	f.Add("KEY", "", false)
	f.Fuzz(func(t *testing.T, name, value string, lower bool) {
		if strings.ToUpper(name) == "ORDINARY" || name == "" || strings.ContainsAny(name, "=\x00") || strings.ContainsRune(value, 0) {
			t.Skip()
		}
		spelling := strings.ToUpper(name)
		if lower {
			spelling = strings.ToLower(name)
		}
		for _, platform := range []string{"windows", "linux", "darwin"} {
			masker := secrets.NewMasker()
			env, held := withholdLocalSecretsOn(platform, []string{spelling + "=" + value, "ORDINARY=value"}, []string{name}, masker)
			match := spelling == name
			if platform == "windows" {
				match = strings.ToUpper(spelling) == strings.ToUpper(name)
			}
			if match {
				if slices.Contains(env, spelling+"="+value) {
					t.Fatalf("%s secret remained in environment", platform)
				}
				if got, ok := held[name]; !ok || got != value {
					t.Fatal("declaration name was lost")
				}
				if len(value) >= 8 && masker.Mask(value) == value {
					t.Fatal("withheld value was not registered for masking")
				}
			} else if !slices.Contains(env, spelling+"="+value) {
				t.Fatal("distinct environment name was withheld")
			}
		}
	})
}
