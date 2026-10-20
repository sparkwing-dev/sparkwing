//go:build !qatrust

package engine

// QAModuleEnvironment returns nothing outside a qatrust build.
func QAModuleEnvironment(Pin, func(args ...string) ([]byte, error)) ([]string, error) {
	return nil, nil
}
