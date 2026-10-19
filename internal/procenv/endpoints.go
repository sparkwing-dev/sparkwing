package procenv

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

func proxyCredentials(name, value string) ([]string, error) {
	var endpoints []string
	switch strings.ToUpper(name) {
	case "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "PIP_INDEX_URL", "PIP_PROXY", "NPM_CONFIG_REGISTRY", "NPM_CONFIG_PROXY", "NPM_CONFIG_HTTPS_PROXY":
		endpoints = []string{value}
	case "GOPROXY":
		endpoints = strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == '|' })
		for i := range endpoints {
			endpoints[i] = strings.TrimSpace(endpoints[i])
		}
	case "PIP_EXTRA_INDEX_URL":
		endpoints = strings.Fields(value)
	case "GOSUMDB":
		if fields := strings.Fields(value); len(fields) > 1 {
			endpoints = fields[1:]
		}
	}
	goEndpoint := strings.EqualFold(name, "GOPROXY") || strings.EqualFold(name, "GOSUMDB")
	var credentials []string
	for _, endpoint := range endpoints {
		parts, err := endpointCredentials(endpoint)
		if err != nil {
			return nil, fmt.Errorf("invalid proxy URL in %q", name)
		}
		credentials = append(credentials, parts...)
		if goEndpoint && pathOrQueryEndpoint(endpoint) {
			credentials = append(credentials, endpoint)
		}
	}
	return credentials, nil
}

// safety: Go module proxies may carry an access token in the path or query, which no userinfo parse finds.
func pathOrQueryEndpoint(endpoint string) bool {
	rest := strings.TrimPrefix(endpoint, "//")
	if _, after, ok := strings.Cut(endpoint, "://"); ok {
		rest = after
	}
	end := strings.IndexAny(rest, "/?#")
	return end >= 0 && strings.Trim(rest[end:], "/") != ""
}

func endpointCredentials(endpoint string) ([]string, error) {
	authority := strings.TrimPrefix(endpoint, "//")
	if scheme, rest, ok := strings.Cut(endpoint, "://"); ok {
		parsedScheme, err := url.Parse(scheme + ":")
		if err == nil && parsedScheme.Scheme != "" && parsedScheme.Opaque == "" {
			authority = rest
		}
	}
	if end := strings.IndexAny(authority, "/?#"); end >= 0 {
		authority = authority[:end]
	}
	if strings.IndexFunc(authority, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return nil, errors.New("control character in proxy authority")
	}
	var rawUser, rawUsername, rawPassword string
	if at := strings.LastIndexByte(authority, '@'); at >= 0 {
		rawUser = authority[:at]
		var hasPassword bool
		rawUsername, rawPassword, hasPassword = strings.Cut(rawUser, ":")
		username, err := url.PathUnescape(rawUsername)
		if err != nil {
			return nil, err
		}
		password, err := url.PathUnescape(rawPassword)
		if err != nil {
			return nil, err
		}
		userinfo := url.User(username)
		if hasPassword {
			userinfo = url.UserPassword(username, password)
		}
		authority = userinfo.String() + authority[at:]
	}
	parsed, err := url.Parse("//" + authority)
	if err != nil || parsed.User == nil {
		return nil, err
	}
	password, hasPassword := parsed.User.Password()
	if parsed.User.Username() == "" && password == "" {
		return nil, nil
	}
	parts := []string{parsed.User.Username(), password, parsed.User.String()}
	if hasPassword {
		parts = append(parts, parsed.User.Username()+":"+password)
	}
	return append(parts, rawUser, rawUsername, rawPassword), nil
}
