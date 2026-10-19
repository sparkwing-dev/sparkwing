//go:build !qatrust

package engine

import "context"

// QAModuleEnvironment returns nothing outside a qatrust build.
func QAModuleEnvironment(context.Context, string, Pin, []string) ([]string, error) {
	return nil, nil
}
