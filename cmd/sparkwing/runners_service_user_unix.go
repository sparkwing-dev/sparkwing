//go:build unix

package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"syscall"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	runnerservice "github.com/sparkwing-dev/sparkwing/internal/runners/service"
)

// runAsServiceAccount runs a staged copy of this CLI as the account, with only
// that account's identity, home and a system PATH.
func runAsServiceAccount(ctx context.Context, account runnerservice.Account, args []string, stdin []byte) ([]byte, error) {
	// safety: no SPARKWING_HOME, so the account's default ~/.sparkwing and
	// ~/.config/sparkwing are the home the config write is allowed to touch.
	env := []string{"HOME=" + account.Home, "USER=" + account.Name, "LOGNAME=" + account.Name, "PATH=/usr/bin:/bin:/usr/sbin:/sbin"}
	cmd := procenv.ControlCommandContext(ctx, env, args[0], args[1:]...)
	cmd.Dir = account.Home
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(account.UID), Gid: uint32(account.GID), Groups: []uint32{uint32(account.GID)}}}
	var stdout, stderr bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(stdin), &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("as %s: %s: %w", account.Name, strings.TrimSpace(stderr.String()), err)
	}
	return stdout.Bytes(), nil
}
