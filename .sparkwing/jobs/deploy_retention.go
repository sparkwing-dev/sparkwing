package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

func (r ecrRegistry) retain(ctx context.Context, password string, pin imagePin) error {
	ref, err := name.NewDigest(pin.Repository + "@" + pin.Digest)
	if err != nil {
		return err
	}
	return retainDeployManifest(ref, r.options(ctx, password)...)
}

func retainDeployManifest(ref name.Digest, options ...remote.Option) error {
	desc, err := remote.Get(ref, options...)
	if err != nil {
		return err
	}
	// safety: platform manifests need their own tags to survive rolling pruning.
	if desc.MediaType.IsIndex() {
		var index struct {
			Manifests []struct {
				Digest string `json:"digest"`
			} `json:"manifests"`
		}
		if err := json.Unmarshal(desc.Manifest, &index); err != nil {
			return err
		}
		for _, child := range index.Manifests {
			digest, err := name.NewDigest(ref.Context().Name() + "@" + child.Digest)
			if err != nil {
				return err
			}
			if err := retainDeployManifest(digest, options...); err != nil {
				return err
			}
		}
	}
	tag := ref.Context().Tag("cloud-deploy-" + desc.Digest.Hex)
	existing, err := remote.Head(tag, options...)
	if err != nil {
		var registryErr *transport.Error
		if !errors.As(err, &registryErr) || registryErr.StatusCode != http.StatusNotFound {
			return err
		}
	}
	if err == nil {
		if existing.Digest != desc.Digest {
			return fmt.Errorf("retention tag %s resolves to %s, expected %s", tag, existing.Digest, desc.Digest)
		}
		return nil
	}
	return remote.Put(tag, desc, options...)
}
