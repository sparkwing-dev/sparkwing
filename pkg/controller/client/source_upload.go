package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/url"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

// UploadSourceSnapshot commits a private snapshot through the controller's shared storage binding.
func UploadSourceSnapshot(ctx context.Context, options EngineOptions, git runspec.Git, code runspec.Source, publication source.Publication) (digestResult string, resultErr error) {
	c, err := newEngineClient(options)
	if err != nil {
		return "", err
	}
	f, cleanup, err := source.OpenPublication(ctx, publication)
	if err != nil {
		return "", err
	}
	defer func() { resultErr = errors.Join(resultErr, cleanup()) }()
	hash := sha256.New()
	size, err := io.Copy(hash, f)
	if err != nil {
		return "", err
	}
	digest := blob.Digest(hex.EncodeToString(hash.Sum(nil)))
	var ticket controllerwire.DataUploadTicket
	err = c.request(ctx, "POST", "/api/v1/data/source", "/api/v1/data/source", "", controllerwire.SourceUploadRequest{Git: git, Source: code, Digest: digest, Size: size}, &ticket)
	if err != nil {
		return "", err
	}
	if !validSelector(ticket.UploadID) || ticket.SHA256 != digest || ticket.Size != size {
		return "", dataProtocolError("source upload ticket differs from snapshot")
	}
	transfer := blob.SignedTransfer{URL: ticket.URL, Headers: ticket.Headers, ExpiresAt: ticket.ExpiresAt, Digest: ticket.SHA256, Size: ticket.Size}
	if err := validateDataTransfer(controllerwire.DataSignedTransfer(transfer)); err != nil {
		return "", err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	body, err := (blob.TransferClient{Transport: c.http.Transport}).Transfer(ctx, transfer, f)
	if err != nil {
		return "", &apierr.Error{Code: apierr.Unavailable, Message: "source snapshot transfer failed"}
	}
	_, copyErr := io.Copy(io.Discard, body)
	if err := errors.Join(copyErr, body.Close()); err != nil {
		return "", &apierr.Error{Code: apierr.Unavailable, Message: "source snapshot transfer did not finish"}
	}
	var committed struct {
		Digest blob.Digest `json:"digest"`
	}
	pathURL := "/api/v1/data/source/" + url.PathEscape(ticket.UploadID) + "/commit"
	if err := c.request(ctx, "POST", "/api/v1/data/source/{upload}/commit", pathURL, "", struct{}{}, &committed); err != nil {
		return "", err
	}
	if committed.Digest != digest {
		return "", dataProtocolError("committed source digest differs from snapshot")
	}
	return string(digest), nil
}
