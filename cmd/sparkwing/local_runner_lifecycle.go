package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
)

type localRunnerLifecycle interface {
	Register(context.Context, localhostwire.LocalRunnerInstance) (localhostwire.LocalRunnerInstance, error)
	Activate(context.Context, string) error
	Watch(context.Context, string, string) (localhostwire.LocalRunnerInstance, error)
	Stop(context.Context, string) error
}

func localRunnerInstance(prefix, version string) (localhostwire.LocalRunnerInstance, error) {
	instance := localhostwire.LocalRunnerInstance{ID: rand.Text(), CapabilityPrefix: prefix, PID: os.Getpid(), Version: version}
	birth, err := procgroup.ProcessBirth(instance.PID)
	if err != nil {
		return instance, err
	}
	instance.ProcessBirth = birth
	executable, err := os.Executable()
	if err != nil {
		return instance, err
	}
	file, err := os.Open(executable)
	if err != nil {
		return instance, err
	}
	digest := sha256.New()
	_, copyErr := io.Copy(digest, file)
	if err := errors.Join(copyErr, file.Close()); err != nil {
		return instance, err
	}
	instance.BuildIdentity = hex.EncodeToString(digest.Sum(nil))
	return instance, nil
}

func watchLocalRunner(ctx context.Context, lifecycle localRunnerLifecycle, id string, loop *runners.Loop) error {
	for {
		instance, err := lifecycle.Watch(ctx, id, "active")
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			loop.Drain()
			return err
		}
		if instance.State != "active" {
			loop.Drain()
			return nil
		}
	}
}
