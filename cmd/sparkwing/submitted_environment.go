package main

import (
	"fmt"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func captureSubmittedEnvironment(environment, allowed []string, masker *secrets.Masker) (map[string][]byte, error) {
	values, err := procenv.Submit(environment, allowed, masker)
	if err != nil {
		return nil, err
	}
	for _, warning := range procenv.SubmitWarnings(allowed) {
		if _, err := fmt.Fprintln(os.Stderr, "warning: "+warning); err != nil {
			return nil, err
		}
	}
	return values, nil
}
