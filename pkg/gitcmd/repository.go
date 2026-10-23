package gitcmd

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

// CloneRepository implements the SDK's user-requested Git clone with its process environment.
func CloneRepository(ctx context.Context, remote, destination string, depth int, environment []string) error {
	if depth < 0 {
		return errors.New("clone depth must not be negative")
	}
	args := []string{"clone"}
	if depth > 0 {
		args = append(args, "--depth", strconv.Itoa(depth))
	}
	args = append(args, "--", remote, destination)
	cmd := procenv.ControlCommandContext(ctx, environment, "git", args...)
	output, err := Output(ctx, cmd, true)
	if err != nil {
		return fmt.Errorf("git clone failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
