package client

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

func TestResolveLocalSecretsResolvesOnlyDeclarations(t *testing.T) {
	var names []string
	values, err := ResolveLocalSecrets(t.Context(), []nodeproto.SecretDeclaration{{Name: "TOKEN"}, {Name: "OPTIONAL", Optional: true}}, func(_ context.Context, name string) (string, bool, error) {
		names = append(names, name)
		if name == "TOKEN" {
			return "", true, nil
		}
		return "", false, nil
	})
	if err != nil || !reflect.DeepEqual(names, []string{"TOKEN", "OPTIONAL"}) || !reflect.DeepEqual(values, map[string]string{"TOKEN": ""}) {
		t.Fatalf("resolved=%#v names=%#v err=%v", values, names, err)
	}
}

func FuzzResolveLocalSecretsNeverReturnsUndeclaredOrFailedValues(f *testing.F) {
	f.Add("sensitive-value", false)
	f.Add("", true)
	f.Add("a", true)
	f.Fuzz(func(t *testing.T, value string, failed bool) {
		declarations := []nodeproto.SecretDeclaration{{Name: "DECLARED"}}
		got, err := ResolveLocalSecrets(t.Context(), declarations, func(_ context.Context, name string) (string, bool, error) {
			if name != "DECLARED" {
				t.Fatal("undeclared lookup")
			}
			if failed {
				return value, true, errors.New("provider failed: " + value)
			}
			return value, true, nil
		})
		if failed {
			if got != nil || err == nil || err.Error() != "requester local secret resolution failed" {
				t.Fatal("failed provider value escaped")
			}
			return
		}
		if err != nil || len(got) != 1 || got["DECLARED"] != value {
			t.Fatal("declared value lost")
		}
	})
}

func TestResolveLocalSecretsRefusesInvalidOrMissingBeforeIntake(t *testing.T) {
	for _, declarations := range [][]nodeproto.SecretDeclaration{{{Name: "TOKEN"}}, {{Name: "../TOKEN"}}, {{Name: "TOKEN"}, {Name: "TOKEN"}}} {
		if got, err := ResolveLocalSecrets(t.Context(), declarations, func(context.Context, string) (string, bool, error) { return "", false, nil }); err == nil || got != nil {
			t.Fatalf("resolved invalid declarations: %#v %v", got, err)
		}
	}
}
