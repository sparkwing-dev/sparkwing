package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/configguard"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
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
		failure = &apierr.Error{Code: apierr.Internal, Message: err.Error()}
		var field *userconfig.FieldError
		var setting *settings.Error
		switch {
		case errors.As(err, &setting):
			failure.Code = apierr.BadRequest
			if setting.CeilingFrom != nil {
				failure.Code = apierr.LimitExceeded
			}
		case errors.As(err, &field), errors.Is(err, configguard.ErrOutsideSandboxHome):
			failure.Code = apierr.BadRequest
		case errors.Is(err, context.Canceled):
			failure.Code = apierr.Cancelled
		case errors.Is(err, context.DeadlineExceeded):
			failure.Code = apierr.DeadlineExceeded
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
	if inv.Output(color.IsInteractiveStdout()) == "json" {
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
