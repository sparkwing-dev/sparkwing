//go:build !windows

package procgroup

// ProcessBirth returns the token that distinguishes this incarnation of pid
// from any later process that reuses the number.
func ProcessBirth(pid int) (string, error) {
	_, token, err := sessionIdentity(pid)
	return token, err
}
