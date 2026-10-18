package main

import (
	"runtime"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

type localRunEnvironment struct {
	names    []string
	captured []string
	held     map[string]string
	masker   *secrets.Masker
}

func prepareRunEnvironment(directory, pipeline string, captured []string, flags runFlags) (*localRunEnvironment, error) {
	if flags.environment != nil {
		return flags.environment, nil
	}
	route, err := loadRunRouting(directory, pipeline)
	if err != nil {
		return nil, err
	}
	return prepareLocalEnvironment(captured, route.LocalSecrets), nil
}

func prepareLocalEnvironment(captured, names []string) *localRunEnvironment {
	e := &localRunEnvironment{names: slices.Clone(names), masker: secrets.NewMasker()}
	e.captured, e.held = withholdLocalSecrets(captured, names, e.masker)
	return e
}

func withholdLocalSecrets(captured, names []string, masker *secrets.Masker) ([]string, map[string]string) {
	return withholdLocalSecretsOn(runtime.GOOS, captured, names, masker)
}

func withholdLocalSecretsOn(goos string, captured, names []string, masker *secrets.Masker) ([]string, map[string]string) {
	declarations := make(map[string]string, len(names))
	for _, name := range names {
		declarations[procenv.NameKey(name, goos)] = name
	}
	held := map[string]string{}
	environment := make([]string, 0, len(captured))
	for _, item := range captured {
		name, value, _ := strings.Cut(item, "=")
		declared, ok := declarations[procenv.NameKey(name, goos)]
		if !ok {
			environment = append(environment, item)
			continue
		}
		masker.Register(value)
		held[declared] = value
	}
	return environment, held
}
