package controller

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func (s *boundData) Download(ctx context.Context, request blob.GetRequest) (blob.SignedTransfer, error) {
	authority, err := s.live(ctx, request.Authority)
	if err != nil {
		return blob.SignedTransfer{}, err
	}
	if !validDataDigest(request.Digest) {
		return blob.SignedTransfer{}, invalidEngineBody("download requires a SHA-256 digest")
	}
	target := capability.DataTarget{Digest: string(request.Digest)}
	if err := s.authorize(ctx, capability.DataDownload, authority, target); err != nil {
		return blob.SignedTransfer{}, err
	}
	if s.controller.ReadBlob == nil || s.controller.Objects == nil || s.controller.ChargeDownload == nil {
		return blob.SignedTransfer{}, missingEndpointPolicy()
	}
	metadata, err := s.controller.ReadBlob(ctx, authority, request.Digest)
	if err != nil {
		return blob.SignedTransfer{}, err
	}
	if metadata.Team != s.authorized.Target.Team || metadata.Digest != request.Digest || metadata.Size < 0 {
		return blob.SignedTransfer{}, dataProjectionError()
	}
	if err := s.controller.ChargeDownload(ctx, authority, metadata); err != nil {
		return blob.SignedTransfer{}, err
	}
	object := capability.ObjectToSign{
		Method: http.MethodGet, Key: "teams/" + metadata.Team + "/cas/" + string(metadata.Digest),
		SHA256: string(metadata.Digest), Size: metadata.Size, ExpiresAt: time.Now().UTC().Add(limits.DataDownloadURLTTL),
	}
	signed, err := s.controller.Objects.Sign(ctx, object)
	if err != nil {
		return blob.SignedTransfer{}, err
	}
	if !signedDataMatches(signed, object) {
		return blob.SignedTransfer{}, dataProjectionError()
	}
	return dataTransfer(signed), nil
}

func (s *boundData) HasMany(ctx context.Context, request blob.HasManyRequest) (blob.Presence, error) {
	if !request.Budget.Deadline.IsZero() {
		if !time.Now().Before(request.Budget.Deadline) {
			return blob.Presence{}, blob.ErrBudget
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, request.Budget.Deadline)
		defer cancel()
	}
	authority, err := s.live(ctx, request.Authority)
	if err != nil {
		return blob.Presence{}, err
	}
	if s.controller.ReadBlob == nil {
		return blob.Presence{}, missingEndpointPolicy()
	}
	for _, digest := range request.Digests {
		if !validDataDigest(digest) {
			return blob.Presence{}, invalidEngineBody("existence queries require SHA-256 digests")
		}
	}
	present := blob.Presence{Present: []blob.Digest{}}
	seen := make(map[blob.Digest]bool, len(request.Digests))
	for _, digest := range request.Digests {
		if seen[digest] {
			continue
		}
		seen[digest] = true
		if err := ctx.Err(); err != nil {
			return blob.Presence{}, dataBudgetError(err, request.Budget)
		}
		if err := s.authorize(ctx, capability.DataHasMany, authority, capability.DataTarget{Digest: string(digest)}); err != nil {
			if dataAbsent(err) {
				continue
			}
			return blob.Presence{}, dataBudgetError(err, request.Budget)
		}
		metadata, err := s.controller.ReadBlob(ctx, authority, digest)
		if dataAbsent(err) {
			continue
		}
		if err != nil {
			return blob.Presence{}, dataBudgetError(err, request.Budget)
		}
		if metadata.Team != s.authorized.Target.Team || metadata.Digest != digest || metadata.Size < 0 {
			return blob.Presence{}, dataProjectionError()
		}
		present.Present = append(present.Present, digest)
	}
	return present, nil
}

func dataAbsent(err error) bool {
	if errors.Is(err, blob.ErrNotFound) || errors.Is(err, blob.ErrDigestNotReadable) || errors.Is(err, store.ErrNotFound) {
		return true
	}
	var failure *apierr.Error
	if !errors.As(err, &failure) {
		return false
	}
	if failure.Code == apierr.BlobMissing || failure.Code == apierr.NotFound {
		return true
	}
	reason := failure.Details["reason"]
	return failure.Code == apierr.Forbidden && (reason == apierr.ReasonDigestNotReadable || reason == string(apierr.ReasonDigestNotReadable))
}

func dataBudgetError(err error, budget blob.Budget) error {
	if !budget.Deadline.IsZero() && errors.Is(err, context.DeadlineExceeded) {
		return blob.ErrBudget
	}
	return err
}
