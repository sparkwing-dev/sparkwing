package storeurl

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// ProfileLookup resolves a profile name (from config.yaml) to its
// controller URL and bearer token. The factory invokes it for
// type=controller specs; other types ignore it. Pass nil when no
// controller-typed spec can appear.
type ProfileLookup func(name string) (controllerURL, token string, err error)

func resolveControllerProfile(surface, controller string, lookup ProfileLookup) (string, string, error) {
	if controller == "" {
		return "", "", fmt.Errorf("%s backend type=controller requires controller: <profile-name>", surface)
	}
	if lookup == nil {
		return "", "", fmt.Errorf("%s backend type=controller needs a profile lookup; caller did not provide one", surface)
	}
	url, token, err := lookup(controller)
	if err != nil {
		return "", "", fmt.Errorf("%s backend profile %q: %w", surface, controller, err)
	}
	if url == "" {
		return "", "", fmt.Errorf("%s backend profile %q resolved to an empty URL", surface, controller)
	}
	return url, token, nil
}

// OpenStateStoreFromSpec constructs a StateStoreOld from a backends.Spec
// that has passed pkg/backends validation. The lookup
// callback is consulted for type=controller specs; pass nil when no
// controller-typed spec can appear.
//
// For type=sqlite, spec.Path is required and names the SQLite database
// file. Callers that want the historical default (~/.sparkwing/state.db)
// should pass that path explicitly so the factory has a single,
// caller-provided source of truth.
func OpenStateStoreFromSpec(ctx context.Context, spec backends.Spec, lookup ProfileLookup) (storage.StateStoreOld, error) {
	switch spec.Type {
	case backends.TypeSQLite:
		path, err := expandPath(spec.Path)
		if err != nil {
			return nil, fmt.Errorf("state sqlite: %w", err)
		}
		return store.Open(path)
	case backends.TypeController:
		url, token, err := resolveControllerProfile("state", spec.Controller, lookup)
		if err != nil {
			return nil, err
		}
		return client.NewWithToken(url, nil, token), nil
	default:
		return nil, fmt.Errorf("state backend type %q is not supported (use sqlite or controller)", spec.Type)
	}
}

func expandPath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("path is required")
	}
	if strings.HasPrefix(p, "~/") || p == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if p == "~" {
			return home, nil
		}
		return home + p[1:], nil
	}
	return p, nil
}
