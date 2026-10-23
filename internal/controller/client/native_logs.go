package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type nativeLogUploader struct {
	transport     *engineClient
	subject       blob.LogSubject
	pattern, path string
	segments      []store.Segment
}

// LogUploader publishes shared log segments through authenticated signing and record commits.
func (c *LocalHostClient) LogUploader(ctx context.Context, principal capability.Principal, subject blob.LogSubject) (logseg.Uploader, error) {
	if err := subject.Validate(); err != nil {
		return nil, err
	}
	if subject.RunID != c.run || subject.Team != principal.Capability.Team {
		return nil, localHostClientRefusal()
	}
	if err := c.live(); err != nil {
		return nil, err
	}
	// Log tails outlive the execution lease. Select the committed bearer here;
	// each signing and record request still authenticates its current log fence.
	transport := c.transport
	raw, err := transport.bearer(ctx)
	if err != nil {
		return nil, err
	}
	if sha256.Sum256([]byte(raw)) != principal.Capability.Digest {
		c.mu.Lock()
		award, ok := c.nodes[principal.Capability.Binding.NodeID]
		c.mu.Unlock()
		if !ok || principal.Capability.Unit != capability.UnitNode || award.prefix != principal.Capability.Prefix || award.generation != principal.Capability.Binding.Generation || sha256.Sum256([]byte(award.raw)) != principal.Capability.Digest {
			return nil, localHostClientRefusal()
		}
		copy := *transport
		copy.bearer = func(context.Context) (string, error) { return award.raw, c.live() }
		transport = &copy
	}
	if principal.Capability.Binding.RunID != c.run || c.node != "" && principal.Capability.Binding.NodeID != c.node {
		return nil, localHostClientRefusal()
	}
	pattern, path := "/api/v1/claims/{claim}", "/api/v1/claims/"+url.PathEscape(principal.Capability.UnitID)
	if subject.Attempt != nil {
		b, attempt := principal.Capability.Binding, subject.Attempt
		if principal.Capability.Unit != capability.UnitNode || attempt.NodeID != b.NodeID || attempt.Generation != b.Generation || attempt.Attempt != b.Attempt {
			return nil, localHostClientRefusal()
		}
		pattern, path = "/api/v1/runs/{run}/jobs/{job}", "/api/v1/runs/"+url.PathEscape(c.run)+"/jobs/"+url.PathEscape(attempt.NodeID)
	} else if subject.Unit == nil || string(subject.Unit.Kind) != string(principal.Capability.Unit) {
		return nil, localHostClientRefusal()
	}
	uploader := &nativeLogUploader{transport: transport, subject: subject, pattern: pattern, path: path}
	// Register before uploading segments so maintenance can seal an interrupted stream incomplete.
	if err := uploader.commit(ctx, nil, false, ""); err != nil {
		return nil, err
	}
	return uploader, nil
}

func (u *nativeLogUploader) signed(ctx context.Context, index int64, body []byte) (blob.LogUploadURL, error) {
	var slot blob.LogUploadURL
	sum := sha256.Sum256(body)
	request := blob.LogSigningRequest{Subject: u.subject, Index: index, Size: int64(len(body)), Digest: blob.Digest(hex.EncodeToString(sum[:]))}
	err := u.transport.request(ctx, "POST", u.pattern+"/log-segments", u.path+"/log-segments", "", request, &slot)
	if err == nil && (!reflect.DeepEqual(slot.Subject, u.subject) || slot.Index != index) {
		err = dataProtocolError("log signing response differs from requested subject or segment")
	}
	return slot, err
}

func (u *nativeLogUploader) put(ctx context.Context, target string, headers map[string]string, expiry time.Time, body []byte) error {
	if err := validateDataTransfer(controllerwire.DataSignedTransfer{URL: target, Headers: headers, ExpiresAt: expiry}); err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, "PUT", target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := u.transport.http.Do(request)
	if err != nil {
		return err
	}
	_, readErr := io.Copy(io.Discard, response.Body)
	err = errors.Join(readErr, response.Body.Close())
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return errors.Join(fmt.Errorf("log upload returned HTTP %d", response.StatusCode), err)
	}
	return err
}

func (u *nativeLogUploader) PutSegment(ctx context.Context, segment logseg.Segment, body []byte) error {
	slot, err := u.signed(ctx, int64(segment.N), body)
	if err != nil {
		return err
	}
	if err := u.put(ctx, slot.URL, slot.Headers, slot.ExpiresAt, body); err != nil {
		return err
	}
	var offset int64
	for _, previous := range u.segments {
		if previous.Index == int64(segment.N) {
			if previous.Digest == segment.SHA256 && previous.StoredBytes == segment.StoredBytes {
				return nil
			}
			return blob.ErrIntegrity
		}
		offset += previous.Size
	}
	key, err := blob.LogObjectKey(u.subject, slot.RetentionClass, int64(segment.N))
	if err != nil {
		return err
	}
	row := store.Segment{Index: int64(segment.N), Offset: offset, Size: segment.Bytes, StoredBytes: segment.StoredBytes, Digest: segment.SHA256, ObjectKey: key}
	if err := u.commit(ctx, []store.Segment{row}, false, ""); err != nil {
		return err
	}
	u.segments = append(u.segments, row)
	return nil
}

func (u *nativeLogUploader) PutManifest(ctx context.Context, manifest logseg.Manifest) error {
	if manifest.Subject == nil || !reflect.DeepEqual(*manifest.Subject, u.subject) {
		return blob.ErrIntegrity
	}
	body, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	slot, err := u.signed(ctx, 0, body)
	if err != nil {
		return err
	}
	if err := u.put(ctx, slot.URL, slot.Headers, slot.ExpiresAt, body); err != nil {
		return err
	}
	reason := ""
	if manifest.Status != logseg.StatusComplete {
		reason = manifest.Reason
	}
	return u.commit(ctx, u.segments, true, reason)
}

func (u *nativeLogUploader) commit(ctx context.Context, segments []store.Segment, final bool, reason string) error {
	var result struct {
		Recorded bool `json:"recorded"`
	}
	body := struct {
		Subject          blob.LogSubject `json:"subject"`
		Segments         []store.Segment `json:"segments"`
		Final            bool            `json:"final"`
		IncompleteReason string          `json:"incomplete_reason,omitempty"`
	}{u.subject, segments, final, reason}
	err := u.transport.request(ctx, "POST", u.pattern+"/log-manifest", u.path+"/log-manifest", "", body, &result)
	if err == nil && !result.Recorded {
		err = dataProtocolError("log segment commit was not acknowledged: " + strconv.FormatBool(final))
	}
	return err
}
