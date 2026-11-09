package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/configguard"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/docs"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

type invocationError struct {
	inv   clisurface.Invocation
	cause error
}

func (e *invocationError) Error() string { return e.cause.Error() }
func (e *invocationError) Unwrap() error { return e.cause }

func writeInvocationError(w io.Writer, err error, encode func(io.Writer, *apierr.Error) error) error {
	inv := clisurface.Invocation{Command: clisurface.Command{Path: "sparkwing"}}
	var invocation *invocationError
	if errors.As(err, &invocation) {
		inv = invocation.inv
	}
	var failure *apierr.Error
	if !errors.As(err, &failure) {
		failure = &apierr.Error{Code: hosterr.Internal, Message: err.Error()}
		var field *userconfig.FieldError
		var setting *settings.Error
		switch {
		case errors.As(err, &setting):
			failure.Code = apierr.BadRequest
			if setting.CeilingFrom != nil {
				failure.Code = hosterr.LimitExceeded
			}
		case errors.As(err, &field), errors.Is(err, configguard.ErrOutsideSandboxHome):
			failure.Code = apierr.BadRequest
		case errors.Is(err, docs.ErrNotFound):
			failure.Code = apierr.NotFound
		case errors.Is(err, context.Canceled):
			failure.Code = hosterr.Cancelled
		case errors.Is(err, context.DeadlineExceeded):
			failure.Code = hosterr.DeadlineExceeded
		}
		var usage *clisurface.UsageError
		if errors.As(err, &usage) {
			failure.Code, failure.Message = apierr.BadRequest, usage.Message
			inv.Command.Path = usage.Path
		}
	}
	if err.Error() != failure.Message && failure.Code != apierr.BadRequest {
		contextual := *failure
		contextual.Message = err.Error()
		failure = &contextual
	}
	if failure.Code == hosterr.Unauthenticated && failure.Hint == "" {
		profile := inv.Value("profile")
		var routed *profileError
		if profile == "" && errors.As(err, &routed) {
			profile = routed.profile
		}
		if profile == "" {
			profile = "NAME"
		}
		quoted := "'" + strings.ReplaceAll(profile, "'", "'\\''") + "'"
		guided := *failure
		guided.Hint = "Sign in again to the dashboard and create a replacement token, then run `sparkwing profiles set --profile " + quoted + " --token-stdin` and paste it on stdin"
		failure = &guided
	}
	if invocationOutput(inv) == "json" {
		return encode(w, failure)
	}
	path := strings.TrimPrefix(inv.Command.Path, "sparkwing")
	prefix := "sparkwing: "
	if path != "" {
		prefix += strings.TrimSpace(path) + ": "
	}
	message := strings.TrimPrefix(failure.Message, prefix)
	message = strings.TrimPrefix(message, strings.TrimSpace(path)+": ")
	if _, err := fmt.Fprintln(w, prefix+message); err != nil {
		return err
	}
	if failure.Hint != "" {
		_, writeErr := fmt.Fprintln(w, "hint: "+failure.Hint)
		return writeErr
	}
	return nil
}

// hack: run picks its profile from machine routing, not --profile, so the dead-token hint reads it here.
type profileError struct {
	profile string
	err     error
}

func (e *profileError) Error() string { return e.err.Error() }
func (e *profileError) Unwrap() error { return e.err }

func withProfile(profile string, err error) error {
	if err == nil {
		return nil
	}
	return &profileError{profile: profile, err: err}
}

type maskedCLIError struct {
	err    error
	masker *secrets.Masker
}

func (e *maskedCLIError) Error() string { return e.masker.Mask(e.err.Error()) }
func (e *maskedCLIError) Unwrap() error { return e.err }

func (e *maskedCLIError) As(target any) bool {
	result, ok := target.(**apierr.Error)
	if !ok {
		return false
	}
	var original *apierr.Error
	if !errors.As(e.err, &original) {
		return false
	}
	*result = e.masker.Error(original)
	return true
}
