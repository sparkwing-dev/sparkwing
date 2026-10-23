package blob

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

// FilesystemOptions binds a private directory and controller URL to a shared signing key.
type FilesystemOptions struct {
	Directory  string
	Endpoint   string
	SigningKey []byte
	Now        func() time.Time
}

// Filesystem serves signed transfers without granting record or read-set authority.
type Filesystem struct {
	root     *os.Root
	endpoint *url.URL
	key      []byte
	now      func() time.Time
}

// NewFilesystem opens the data directory without consulting process credentials.
func NewFilesystem(options FilesystemOptions) (*Filesystem, error) {
	u, err := url.Parse(options.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(options.SigningKey) < sha256.Size || options.Directory == "" {
		return nil, fmt.Errorf("filesystem requires a directory, HTTP endpoint and signing key of at least 32 bytes")
	}
	if err := os.MkdirAll(options.Directory, 0o700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(options.Directory)
	if err != nil {
		return nil, err
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &Filesystem{root: root, endpoint: u, key: append([]byte(nil), options.SigningKey...), now: options.Now}, nil
}

// Close releases the directory handle after the server has drained its requests.
func (f *Filesystem) Close() error { return f.root.Close() }

// Sign binds one authorized key, method, size, digest and expiry to a transfer.
func (f *Filesystem) Sign(ctx context.Context, object capability.ObjectToSign) (capability.SignedObject, error) {
	if err := ctx.Err(); err != nil {
		return capability.SignedObject{}, err
	}
	if err := validateObject(object, f.now()); err != nil {
		return capability.SignedObject{}, err
	}
	return f.signObject(object)
}

func (f *Filesystem) signObject(object capability.ObjectToSign) (capability.SignedObject, error) {
	encoded, err := json.Marshal(object)
	if err != nil {
		return capability.SignedObject{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(encoded)
	u := *f.endpoint
	u.Path, u.RawPath = strings.TrimRight(f.endpoint.Path, "/")+f.transferPath(object), ""
	u.RawQuery = url.Values{"token": {token}, "signature": {f.signature(token)}}.Encode()
	return capability.SignedObject{URL: u.String(), Size: object.Size, SHA256: object.SHA256, ExpiresAt: object.ExpiresAt}, nil
}

func (f *Filesystem) signature(token string) string {
	mac := hmac.New(sha256.New, f.key)
	_, _ = io.WriteString(mac, f.endpoint.Path+"\n"+token) //nolint:errcheck // hash.Hash.Write cannot fail.
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (f *Filesystem) signedRequest(r *http.Request) (capability.ObjectToSign, error) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q) != 2 || len(q["token"]) != 1 || len(q["signature"]) != 1 {
		return capability.ObjectToSign{}, ErrDigestNotReadable
	}
	token := q.Get("token")
	if !hmac.Equal([]byte(q.Get("signature")), []byte(f.signature(token))) {
		return capability.ObjectToSign{}, ErrDigestNotReadable
	}
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return capability.ObjectToSign{}, ErrDigestNotReadable
	}
	var object capability.ObjectToSign
	if err := json.Unmarshal(b, &object); err != nil {
		return object, ErrDigestNotReadable
	}
	validate := validateObject
	if strings.HasPrefix(object.Key, "logs/") {
		validate = validateFilesystemLog
	}
	if r.Method != object.Method || validate(object, f.now()) != nil || r.URL.Path != f.transferPath(object) {
		return object, ErrDigestNotReadable
	}
	return object, nil
}

func (f *Filesystem) transferPath(object capability.ObjectToSign) string {
	if strings.HasPrefix(object.Key, "logs/") {
		return "/api/v1/log-segments/" + object.Key
	}
	if object.Method == http.MethodPut {
		return "/api/v1/data/uploads/" + path.Base(object.Key)
	}
	return "/api/v1/data/objects/" + object.Key
}

// VerifyTransfer checks the signed method, path, object and expiry before transfer.
func (f *Filesystem) VerifyTransfer(r *http.Request) error {
	_, err := f.signedRequest(r)
	return err
}

// ServeHTTP accepts signed PUT and GET only. Raw object paths never authorize access.
func (f *Filesystem) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	object, err := f.signedRequest(r)
	if err != nil {
		if err := apierr.WriteHTTP(w, apierr.Authorization(apierr.ReasonBinding, "invalid object signature")); err != nil {
			panic(http.ErrAbortHandler) //nolint:forbidigo // net/http aborts the incomplete response.
		}
		return
	}
	if object.Method == http.MethodPut {
		if err := f.put(r, object); err != nil {
			apierr.NoteResponseError(w, err)
			code := apierr.Internal
			if errors.Is(err, ErrIntegrity) {
				code = apierr.BadRequest
			}
			if err := apierr.WriteHTTP(w, &apierr.Error{Code: code, Message: "object upload failed"}); err != nil {
				panic(http.ErrAbortHandler) //nolint:forbidigo // net/http aborts the incomplete response.
			}
			return
		}
		w.WriteHeader(http.StatusCreated)
		return
	}
	f.get(w, r, object)
}

func (f *Filesystem) put(r *http.Request, object capability.ObjectToSign) (err error) {
	if r.ContentLength >= 0 && r.ContentLength != object.Size {
		return ErrIntegrity
	}
	if err := f.root.MkdirAll(path.Dir(object.Key), 0o700); err != nil {
		return err
	}
	temp := object.Key + "-" + rand.Text()
	file, err := f.root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.root.Remove(temp)) }()
	rd := newVerifiedReader(r.Context(), r.Body, SignedTransfer{Digest: Digest(object.SHA256), Size: object.Size})
	_, err = io.Copy(file, &contextReader{ctx: r.Context(), reader: rd})
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	if !rd.Verified() {
		return ErrIntegrity
	}
	if err := f.root.Link(temp, object.Key); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		return f.verify(r.Context(), object.Key, Blob{Digest: Digest(object.SHA256), Size: object.Size})
	}
	return nil
}

func (f *Filesystem) get(w http.ResponseWriter, r *http.Request, object capability.ObjectToSign) {
	file, err := f.root.Open(object.Key)
	if err != nil {
		if err := apierr.WriteHTTP(w, &apierr.Error{Code: apierr.NotFound, Message: "object unavailable"}); err != nil {
			panic(http.ErrAbortHandler) //nolint:forbidigo // net/http aborts the incomplete response.
		}
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != object.Size {
		if err := apierr.WriteHTTP(w, &apierr.Error{Code: apierr.Conflict, Message: "object integrity mismatch"}); err != nil {
			panic(http.ErrAbortHandler) //nolint:forbidigo // net/http aborts the incomplete response.
		}
		return
	}
	w.Header().Set("Content-Length", strconv.FormatInt(object.Size, 10))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	// safety: clients verify EOF; a failed copy must not look like a complete response.
	if _, err := io.Copy(w, &contextReader{ctx: r.Context(), reader: file}); err != nil {
		panic(http.ErrAbortHandler) //nolint:forbidigo // net/http catches this sentinel and aborts the incomplete response.
	}
}

func (f *Filesystem) verify(ctx context.Context, key string, expected Blob) error {
	body, err := f.root.Open(key)
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return verifyObject(ctx, body, expected)
}

// CommitObject verifies this ticket's staged bytes before publishing its immutable CAS key.
func (f *Filesystem) CommitObject(ctx context.Context, pending string, expected Blob) error {
	final, err := ObjectKey(expected.Team, expected.Digest)
	if err != nil {
		return err
	}
	if expected.Size < 0 {
		return ErrIntegrity
	}
	if err := validatePending(pending, expected.Team); err != nil {
		return err
	}
	if err := f.verify(ctx, pending, expected); err != nil {
		return err
	}
	if err := f.root.MkdirAll(path.Dir(final), 0o700); err != nil {
		return err
	}
	if err := f.root.Link(pending, final); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		return f.verify(ctx, final, expected)
	}
	return nil
}

// DeleteObject removes a key after the caller commits retention or staging cleanup.
func (f *Filesystem) DeleteObject(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateDeleteKey(key); err != nil {
		return err
	}
	err := f.root.Remove(key)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// DeleteBlob removes a CAS object after the leased collector's reachability recheck.
func (f *Filesystem) DeleteBlob(ctx context.Context, object Blob) error {
	key, err := ObjectKey(object.Team, object.Digest)
	if err != nil {
		return err
	}
	return f.DeleteObject(ctx, key)
}

var _ ObjectBackend = (*Filesystem)(nil)
