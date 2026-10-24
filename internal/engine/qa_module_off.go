//go:build !qatrust

package engine

import "github.com/sparkwing-dev/sparkwing/internal/engineid"

// QAModuleEnvironment returns nothing outside a qatrust build.
func QAModuleEnvironment(engineid.Pin, func(args ...string) ([]byte, error)) ([]string, error) {
	return nil, nil
}
