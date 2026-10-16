package main

import (
	"context"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

func lookupTriggerGitHubID(ctx context.Context, remote string, captured []string) (int64, error) {
	env := make(map[string]string)
	for _, item := range captured {
		key, value, _ := strings.Cut(item, "=")
		env[key] = value
	}
	token := env["GH_TOKEN"]
	if token == "" {
		token = env["GITHUB_TOKEN"]
	}
	return sourceurl.GitHubRepositoryID(ctx, remote, token)
}
