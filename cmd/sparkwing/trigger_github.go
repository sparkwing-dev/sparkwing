package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

func lookupTriggerGitHubID(ctx context.Context, remote string, captured []string) (int64, error) {
	identity, err := sourceurl.Identity(remote)
	if err != nil {
		return 0, &apierr.Error{Code: apierr.InvalidArgs, Message: "invalid GitHub repository URL"}
	}
	repo, ok := strings.CutPrefix(identity, "github.com/")
	if !ok || len(strings.Split(repo, "/")) != 2 {
		return 0, &apierr.Error{Code: apierr.InvalidArgs, Message: "GitHub origin must name an owner and repository"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+repo, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	env := make(map[string]string)
	for _, item := range captured {
		key, value, _ := strings.Cut(item, "=")
		env[key] = value
	}
	token := env["GH_TOKEN"]
	if token == "" {
		token = env["GITHUB_TOKEN"]
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, &apierr.Error{Code: apierr.Unavailable, Message: "GitHub repository identity lookup failed"}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		code := apierr.Unavailable
		switch response.StatusCode {
		case 401:
			code = apierr.Unauthenticated
		case 403:
			code = apierr.Forbidden
		case 404:
			code = apierr.NotFound
		case 429:
			code = apierr.RateLimited
		}
		return 0, &apierr.Error{Code: code, Message: fmt.Sprintf("GitHub repository identity lookup returned HTTP %d", response.StatusCode), Hint: "For a private repository, supply GH_TOKEN or GITHUB_TOKEN with repository read access."}
	}
	var result struct {
		ID int64 `json:"id"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if err := decoder.Decode(&result); err != nil || result.ID <= 0 {
		return 0, &apierr.Error{Code: apierr.ProtocolError, Message: "GitHub repository identity response has no positive numeric ID"}
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return 0, &apierr.Error{Code: apierr.ProtocolError, Message: "GitHub repository identity response is malformed"}
	}
	return result.ID, nil
}
