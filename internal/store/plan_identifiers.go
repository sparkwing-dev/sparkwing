package store

import (
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
)

func checkExecutionIdentifiers(execution runspec.ExecutionSettings, matcher nodeproto.SecretMatcher) error {
	return settings.VisitExecutionIdentifiers(execution, func(value, path string) error { return nodeproto.ValidateIdentifierSecret(value, path, matcher) })
}
