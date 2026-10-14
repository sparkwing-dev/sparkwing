package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

var daemonLifecycleNotices = []string{"taking over daemon"}

func announceDaemonLifecycle(format string, args ...any) {
	for _, prefix := range daemonLifecycleNotices {
		if strings.HasPrefix(format, prefix) {
			fmt.Fprintf(os.Stderr, "sparkwing run: "+format+"\n", args...)
			return
		}
	}
}

const ensureDaemonTimeout = 10 * time.Second

func withWingdHost(env []string) []string {
	if os.Getenv(wingdclient.HostBinEnv) != "" {
		return env
	}
	self, err := os.Executable()
	if err != nil {
		return env
	}
	return setEnv(env, wingdclient.HostBinEnv, self)
}

var ensureRunDaemonFn = ensureRunDaemon

func ensureRunDaemon() {
	sock, err := wingd.SocketPath("")
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), ensureDaemonTimeout)
	defer cancel()

	info, perr := wingdclient.Probe(ctx, sock)
	switch {
	case perr == nil && info.Draining:

		return
	case perr != nil && !errors.Is(perr, wingdclient.ErrNoDaemon):

		return
	}

	cl, err := wingdclient.EnsureDaemon(ctx, wingdclient.Options{
		Version: installedVersion(),
		Logf:    announceDaemonLifecycle,
	})
	if err != nil {
		return
	}
	_ = cl.Close()
}
