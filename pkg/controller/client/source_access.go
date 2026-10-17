package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/source"
)

type sourceAccess struct {
	transport *engineClient
	run       string
	sealed    source.Spec
}

// NewSourceAccess binds credential release and bundle reads to sealed source input.
func NewSourceAccess(options EngineOptions, run string, sealed source.Spec) (source.Access, error) {
	if run == "" || sealed.RepoKey == "" {
		return nil, errors.New("source access requires a run and sealed repository")
	}
	transport, err := newEngineClient(options)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(sealed)
	if err != nil {
		return nil, err
	}
	var captured source.Spec
	if err := json.Unmarshal(data, &captured); err != nil {
		return nil, err
	}
	return &sourceAccess{transport: transport, run: run, sealed: captured}, nil
}

func (s *sourceAccess) GitCredential(ctx context.Context, spec source.Spec) (source.Credential, error) {
	if !reflect.DeepEqual(spec, s.sealed) && (s.sealed.PipelineSHA == "" || !reflect.DeepEqual(spec, s.sealed.PipelineSource())) {
		return source.Credential{}, localHostClientRefusal()
	}
	var result struct {
		Kind       string `json:"kind"`
		Host       string `json:"host"`
		Username   string `json:"username,omitempty"`
		Secret     string `json:"secret"`
		KnownHosts string `json:"known_hosts,omitempty"`
	}
	err := s.transport.request(ctx, "POST", "/api/v1/runs/{run}/source-credential", "/api/v1/runs/"+url.PathEscape(s.run)+"/source-credential", "", nil, &result)
	if err != nil {
		return source.Credential{}, err
	}
	return source.Credential{Kind: result.Kind, Host: result.Host, Username: result.Username, Secret: result.Secret, KnownHosts: result.KnownHosts}, nil
}

func (s *sourceAccess) BundleURL(ctx context.Context, digest string) (string, error) {
	if digest == "" || digest != s.sealed.BundleDigest {
		return "", localHostClientRefusal()
	}
	var result struct {
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
		Digest  string            `json:"sha256"`
		Expires time.Time         `json:"expires"`
	}
	err := s.transport.request(ctx, "POST", "/api/v1/data/download", "/api/v1/data/download", "", struct {
		Digest string `json:"digest"`
	}{digest}, &result)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(result.URL)
	if err != nil || parsed.Scheme != "https" && parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" || result.Digest != digest || !result.Expires.After(time.Now()) || len(result.Headers) != 0 {
		return "", dataProtocolError("source bundle requires a live matching signed URL without headers")
	}
	return result.URL, nil
}
