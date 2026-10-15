package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
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
