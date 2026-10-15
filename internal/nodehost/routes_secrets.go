package nodehost

import (
	"context"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/secretsource"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const resolveTimeout = 10 * time.Second

func (g *grant) secret(ctx context.Context, name string) (string, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	value, masked, err := g.Resolver.Resolve(ctx, name)
	if errors.Is(err, secretsource.ErrMissing) || errors.Is(err, secrets.ErrSecretMissing) {
		return "", false, executorError(apierr.NotFound, "secret is not set")
	}
	if err != nil {
		return "", false, serviceUnavailable()
	}
	return value, masked, nil
}

func (g *grant) annotation(_ context.Context, _ capability.Authorized, request nodeproto.AnnotationRequest) error {
	if g.Sink == nil {
		return serviceUnavailable()
	}
	record := sparkwing.LogRecord{Level: request.Level, Event: "job_annotation", Msg: request.Message, Step: request.Step, Attrs: map[string]any{"file": request.File, "line": request.Line, "end_line": request.EndLine, "column": request.Column}}
	if err := g.Sink.persist(record); err != nil {
		return err
	}
	g.Sink.emit(record)
	return nil
}

func (g *grant) summary(_ context.Context, _ capability.Authorized, request nodeproto.SummaryRequest) error {
	if g.Sink == nil {
		return serviceUnavailable()
	}
	record := sparkwing.LogRecord{Level: "info", Event: "job_summary", Msg: request.Markdown, Step: request.Step}
	if err := g.Sink.persist(record); err != nil {
		return err
	}
	g.Sink.emit(record)
	return nil
}
