package controller

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type directUploadS3 struct {
	client  *s3.Client
	presign *s3.PresignClient
	bucket  string
	prefix  string
}

// WithDirectUploads configures S3 output and log transfers.
func (s *Server) WithDirectUploads(client *s3.Client, bucket, prefix string) *Server {
	if client != nil && bucket != "" && prefix != "" {
		s.directUploads = &directUploadS3{
			client: client, presign: s3.NewPresignClient(client,
				func(o *s3.PresignOptions) {
					o.Presigner = v4.NewSigner(func(s *v4.SignerOptions) { s.DisableHeaderHoisting = true })
				}),
			bucket: bucket, prefix: strings.Trim(prefix, "/"),
		}
	}
	return s
}

// EnvDefaultBranch carries, on a GitHub App trigger, the repository's default
// branch, which a claim's cache reads fall back to.
const EnvDefaultBranch = "GITHUB_DEFAULT_BRANCH"

func (s *Server) triggerCacheScope(ctx context.Context, trigger *store.Trigger) (repo, write string, read []string, err error) {
	repoID, err := s.store.TriggerRepoID(ctx, trigger.Team, trigger.ID)
	if err != nil {
		return "", "", nil, err
	}
	if repoID > 0 {
		repo = "github:" + strconv.FormatInt(repoID, 10)
	}
	env := trigger.TriggerEnv
	own := triggerOwnRef(trigger)
	refs := []string{}
	for _, ref := range []string{own, branchRef(env[sparkwing.EnvPRBaseRef]), branchRef(env[EnvDefaultBranch])} {
		if ref != "" && !slices.Contains(refs, ref) {
			refs = append(refs, ref)
		}
	}
	return repo, own, refs, nil
}

func triggerOwnRef(trigger *store.Trigger) string {
	if ref := trigger.TriggerEnv["GITHUB_REF"]; ref != "" {
		return ref
	}
	return branchRef(trigger.GitBranch)
}

func branchRef(branch string) string {
	if branch == "" {
		return ""
	}
	return "refs/heads/" + branch
}

func (d *directUploadS3) pendingKey(id string) string { return "pending/" + id }
func (d *directUploadS3) finalKey(u store.Upload) string {
	return d.prefix + "/teams/" + string(u.Team) + "/" + u.Provenance + "/" + u.Key
}

const directUploadPutTTL = 15 * time.Minute

func (d *directUploadS3) presignPut(ctx context.Context, u store.Upload, sha []byte) (string, map[string]string, error) {
	checksum := base64.StdEncoding.EncodeToString(sha)
	put, err := d.presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(d.bucket), Key: aws.String(d.pendingKey(u.ID)),
		ContentLength: aws.Int64(u.Size), ChecksumSHA256: aws.String(checksum),
	}, func(o *s3.PresignOptions) { o.Expires = directUploadPutTTL })
	if err != nil {
		return "", nil, err
	}
	headers := map[string]string{"Content-Length": fmt.Sprint(u.Size), "x-amz-checksum-sha256": checksum}
	for k, v := range put.SignedHeader {
		if len(v) > 0 {
			headers[k] = v[0]
		}
	}
	return put.URL, headers, nil
}

// safety: a client fixes these by uploading again; any other commit failure
// is the controller's and answers 500.
var (
	errUploadConflict = errors.New("upload conflict")
	errUploadMismatch = errors.New("upload mismatch")
)

func (s *Server) writeCommitResult(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, errUploadConflict), errors.Is(err, store.ErrObjectExists):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, errUploadMismatch):
		writeError(w, http.StatusUnprocessableEntity, err)
	case errors.Is(err, store.ErrFreeStoragePaused):
		writeError(w, http.StatusPaymentRequired, err)
	default:
		s.writeInternalError(w, r, "commit direct upload", err)
	}
}

func (d *directUploadS3) commit(ctx context.Context, st *store.Store, u store.Upload, logger *slog.Logger) error {
	head, err := d.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(d.bucket), Key: aws.String(d.pendingKey(u.ID)), ChecksumMode: types.ChecksumModeEnabled,
	})
	if err != nil {
		var api smithy.APIError
		if errors.As(err, &api) && (api.ErrorCode() == "NotFound" || api.ErrorCode() == "NoSuchKey") {
			return fmt.Errorf("%w: the pending upload is absent", errUploadConflict)
		}
		return fmt.Errorf("head pending upload: %w", err)
	}
	raw, err := hex.DecodeString(u.SHA256)
	if err != nil {
		return fmt.Errorf("decode reserved upload checksum: %w", err)
	}
	if aws.ToInt64(head.ContentLength) != u.Size || aws.ToString(head.ChecksumSHA256) != base64.StdEncoding.EncodeToString(raw) {
		return fmt.Errorf("%w: pending object size or sha256 checksum does not match the declaration", errUploadMismatch)
	}
	final := d.finalKey(u)
	recordedUploader := u.Principal
	_, err = d.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket: aws.String(d.bucket), Key: aws.String(final),
		CopySource:  aws.String(url.PathEscape(d.bucket + "/" + d.pendingKey(u.ID))),
		IfNoneMatch: aws.String("*"), MetadataDirective: types.MetadataDirectiveReplace,
		Metadata: map[string]string{
			"uploader": u.Principal, "provenance": u.Provenance,
			"sha256": u.SHA256, "upload-id": u.ID,
		},
	})
	if err != nil {
		var api smithy.APIError
		if !errors.As(err, &api) || api.ErrorCode() != "PreconditionFailed" {
			return fmt.Errorf("copy committed object: %w", err)
		}
		previous, herr := d.client.HeadObject(ctx, &s3.HeadObjectInput{
			Bucket: aws.String(d.bucket), Key: aws.String(final), ChecksumMode: types.ChecksumModeEnabled,
		})
		if herr != nil || aws.ToInt64(previous.ContentLength) != u.Size ||
			aws.ToString(previous.ChecksumSHA256) != base64.StdEncoding.EncodeToString(raw) ||
			previous.Metadata["sha256"] != u.SHA256 || previous.Metadata["provenance"] != u.Provenance ||
			previous.Metadata["uploader"] == "" ||
			(strings.HasPrefix(u.Key, "sources/") &&
				(previous.Metadata["uploader"] != u.Principal || previous.Metadata["upload-id"] != u.ID)) {
			return store.ErrObjectExists
		}
		recordedUploader = previous.Metadata["uploader"]
	}
	if err := st.CommitUpload(ctx, u.Team, u.ID, recordedUploader, time.Now()); err != nil {
		return err
	}
	_, err = d.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(d.bucket), Key: aws.String(d.pendingKey(u.ID))})
	if err != nil {
		logger.Warn("delete committed pending object", "upload_id", u.ID, "err", err)
	}
	return nil
}
