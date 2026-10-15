package controller

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// GitCredentialJSON is a team git credential as the API shows it: never its
// secret. Fingerprint and HostKey are the ssh host key the credential pins,
// which the owner confirms before the credential is released.
type GitCredentialJSON struct {
	Host        string `json:"host"`
	Kind        string `json:"kind"`
	Username    string `json:"username,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	HostKey     string `json:"host_key,omitempty"`
	// PublicKey is the deploy key's public half, to add to the forge.
	PublicKey string `json:"public_key,omitempty"`
	Confirmed bool   `json:"confirmed"`
	CreatedBy string `json:"created_by"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

type gitCredentialPutReq struct {
	Host       string `json:"host"`
	Kind       string `json:"kind"`
	Port       *int   `json:"port,omitempty"`
	PrivateKey string `json:"private_key,omitempty"`
	Username   string `json:"username,omitempty"`
	Token      string `json:"token,omitempty"`
}

type gitCredentialConfirmReq struct {
	Fingerprint string `json:"fingerprint"`
}

// GitCredentialReleaseJSON is one audit row of a release.
type GitCredentialReleaseJSON struct {
	Host        string `json:"host"`
	RunID       string `json:"run_id"`
	Runner      string `json:"runner"`
	TokenPrefix string `json:"token_prefix"`
	ReleasedAt  int64  `json:"released_at"`
}

const (
	maxDeployKeyBytes     = 16 << 10
	defaultHTTPSUsername  = "x-access-token"
	hostKeyScanTimeout    = 10 * time.Second
	gitCredentialScope    = "git-credential"
	gitCredentialListSize = 200
)

func gitCredentialJSON(c store.GitCredential, publicKey string) GitCredentialJSON {
	return GitCredentialJSON{
		Host: c.Host, Kind: c.Kind, Username: c.Username, Fingerprint: c.Fingerprint, HostKey: c.KnownHosts,
		PublicKey: publicKey, Confirmed: c.ConfirmedAt != nil, CreatedBy: c.CreatedBy,
		CreatedAt: c.CreatedAt.Unix(), UpdatedAt: c.UpdatedAt.Unix(),
	}
}

// safety: the envelope is bound to its team and host, so a row copied to
// another team or host, or into the secrets table, does not open.
func gitCredentialBinding(team store.Team, host string) secretBinding {
	return secretBinding{Team: team, Name: gitCredentialScope + ":" + host, Scope: gitCredentialScope, Masked: true}
}

func (s *Server) handleListGitCredentials(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	t, err := s.credentialInventory(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	creds, err := t.GitCredentials(r.Context())
	if err != nil {
		return engineLookupError(err)
	}
	out := make([]GitCredentialJSON, 0, len(creds))
	for _, c := range creds {
		out = append(out, gitCredentialJSON(c, ""))
	}
	writeJSON(w, http.StatusOK, map[string]any{"credentials": out})
	return nil
}

func (s *Server) handlePutGitCredential(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	req, err := decodeCredentialRequest[gitCredentialPutReq](r)
	if err != nil {
		return err
	}
	t, err := s.credentialWriter(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	cipher, ok := s.secretsCipher.(BoundCipher)
	if !ok {
		return &apierr.Error{Code: apierr.Unavailable, Message: "bound secret encryption is unavailable", Details: map[string]any{"reason": apierr.ReasonSecretsKeyRequired}}
	}
	host := strings.ToLower(strings.TrimSpace(req.Host))
	if !validGitCredentialHost(host) {
		return invalidEngineBody("host must be a hostname with no scheme, port or path")
	}
	cred := store.GitCredential{Team: t.Team(), Host: host, Kind: req.Kind, CreatedBy: a.Principal.Capability.Subject.ID}
	var secret, publicKey string
	switch req.Kind {
	case store.GitCredentialSSH:
		signer, err := parseDeployKey(req.PrivateKey)
		if err != nil {
			return invalidEngineBody(err.Error())
		}
		port := 22
		if req.Port != nil {
			port = *req.Port
		}
		if port < 1 || port > 65535 {
			return invalidEngineBody("port must be an ssh port between 1 and 65535")
		}
		key, err := s.scanHostKey(r.Context(), host, port)
		if err != nil {
			return &apierr.Error{Code: apierr.Unavailable, Message: "could not read the ssh host key"}
		}
		addr := host
		if port != 22 {
			addr = net.JoinHostPort(host, strconv.Itoa(port))
		}
		cred.KnownHosts = knownhosts.Line([]string{knownhosts.Normalize(addr)}, key)
		cred.Fingerprint = ssh.FingerprintSHA256(key)
		secret = strings.TrimRight(req.PrivateKey, "\n") + "\n"
		publicKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	case store.GitCredentialHTTPS:
		cred.Username = strings.TrimSpace(req.Username)
		if cred.Username == "" {
			cred.Username = defaultHTTPSUsername
		}
		if !credentialProtocolValue(cred.Username) || len(cred.Username) > 128 {
			return invalidEngineBody("username is up to 128 printable characters with no space")
		}
		if !credentialProtocolValue(req.Token) {
			return invalidEngineBody("token is up to 1024 printable characters with no space")
		}
		secret = req.Token
	default:
		return invalidEngineBody("kind is ssh or https")
	}
	sealed, err := sealSecret(cipher, gitCredentialBinding(t.Team(), host), secret)
	if err != nil {
		return credentialWriteError(err)
	}
	cred.Secret = sealed
	stored, err := t.WriteGitCredential(r.Context(), a, cred)
	if err != nil {
		return credentialWriteError(err)
	}
	if stored.Team != t.Team() || stored.Host != host {
		return errors.New("invalid persisted git credential")
	}
	writeJSON(w, http.StatusOK, gitCredentialJSON(stored, publicKey))
	return nil
}

func (s *Server) handleConfirmGitCredential(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	req, err := decodeCredentialRequest[gitCredentialConfirmReq](r)
	if err != nil {
		return err
	}
	if strings.TrimSpace(req.Fingerprint) == "" {
		return invalidEngineBody("fingerprint is required")
	}
	t, err := s.credentialWriter(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	host := r.PathValue("host")
	c, err := t.ConfirmGitCredentialFor(r.Context(), a, host, strings.TrimSpace(req.Fingerprint))
	if err != nil {
		return credentialWriteError(err)
	}
	if c.Team != t.Team() || c.Host != host {
		return errors.New("invalid persisted git credential")
	}
	writeJSON(w, http.StatusOK, gitCredentialJSON(c, ""))
	return nil
}

func (s *Server) handleDeleteGitCredential(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	t, err := s.credentialWriter(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	host := r.PathValue("host")
	if err := t.RemoveGitCredential(r.Context(), a, host); err != nil {
		return credentialWriteError(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) handleListGitCredentialReleases(w http.ResponseWriter, r *http.Request) {
	_, t, ok := s.teamMember(w, r, store.RoleOwner)
	if !ok {
		return
	}
	rels, err := t.GitCredentialReleases(r.Context(), gitCredentialListSize)
	if err != nil {
		s.writeInternalError(w, r, "list git credential releases", err)
		return
	}
	out := make([]GitCredentialReleaseJSON, 0, len(rels))
	for _, rel := range rels {
		out = append(out, GitCredentialReleaseJSON{
			Host: rel.Host, RunID: rel.RunID, Runner: rel.Runner, TokenPrefix: rel.TokenPrefix,
			ReleasedAt: rel.ReleasedAt.Unix(),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"releases": out})
}

func validGitCredentialHost(host string) bool {
	if host == "" || len(host) > 253 || strings.HasPrefix(host, "-") || strings.HasPrefix(host, ".") ||
		strings.HasSuffix(host, ".") || !strings.Contains(host, ".") {
		return false
	}
	for _, c := range host {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '.' {
			return false
		}
	}
	return true
}

func credentialProtocolValue(v string) bool {
	if v == "" || len(v) > 1024 {
		return false
	}
	for _, c := range v {
		if c <= ' ' || c > '~' {
			return false
		}
	}
	return true
}

func parseDeployKey(pemKey string) (ssh.Signer, error) {
	if len(pemKey) > maxDeployKeyBytes || !strings.Contains(pemKey, "PRIVATE KEY") {
		return nil, errors.New("private_key is an unencrypted PEM or OpenSSH private key")
	}
	signer, err := ssh.ParsePrivateKey([]byte(pemKey))
	var missing *ssh.PassphraseMissingError
	if errors.As(err, &missing) {
		return nil, errors.New("private_key is protected by a passphrase; remove it, since a runner cannot type one")
	}
	if err != nil {
		return nil, errors.New("private_key does not parse as an ssh private key")
	}
	return signer, nil
}

func (s *Server) scanHostKey(ctx context.Context, host string, port int) (ssh.PublicKey, error) {
	if s.hostKeyScan != nil {
		return s.hostKeyScan(ctx, host, port)
	}
	return scanHostKey(ctx, host, port)
}

var errHostKeyRead = errors.New("host key read")

// safety: An owner-named host cannot make the controller probe its own network.
func scanHostKey(ctx context.Context, host string, port int) (ssh.PublicKey, error) {
	var d net.Dialer
	return scanHostKeyVia(ctx, host, port, net.DefaultResolver.LookupIPAddr, d.DialContext)
}

// safety: the name is resolved once, and the scan dials the address that
// passed the check, so a resolver that answers differently the second time
// cannot steer the dial inward. The name stays the host key's.
func scanHostKeyVia(ctx context.Context, host string, port int, lookup sourceurl.Lookup,
	dial func(ctx context.Context, network, addr string) (net.Conn, error),
) (ssh.PublicKey, error) {
	ctx, cancel := context.WithTimeout(ctx, hostKeyScanTimeout)
	defer cancel()
	var resolved []net.IPAddr
	resolveOnce := func(ctx context.Context, name string) ([]net.IPAddr, error) {
		addrs, err := lookup(ctx, name)
		resolved = addrs
		return addrs, err
	}
	if err := sourceurl.CheckResolvedHost(ctx, "ssh://git@"+host+"/scan", resolveOnce); err != nil {
		return nil, err
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		if len(resolved) == 0 {
			return nil, fmt.Errorf("%s does not resolve", host)
		}
		ip = resolved[0].IP
	}
	conn, err := dial(ctx, "tcp", net.JoinHostPort(ip.String(), strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	return readHostKey(ctx, conn, net.JoinHostPort(host, strconv.Itoa(port)))
}

func readHostKey(ctx context.Context, conn net.Conn, addr string) (_ ssh.PublicKey, err error) {
	defer func() {
		if cerr := conn.Close(); cerr != nil && !errors.Is(cerr, net.ErrClosed) && err == nil {
			err = cerr
		}
	}()
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return nil, err
		}
	}
	var got ssh.PublicKey
	cfg := &ssh.ClientConfig{
		User: "git",
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			got = key
			return errHostKeyRead
		},
		HostKeyAlgorithms: []string{
			ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521,
			ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256,
		},
		Timeout: hostKeyScanTimeout,
	}
	_, _, _, err = ssh.NewClientConn(conn, addr, cfg)
	if got != nil {
		return got, nil
	}
	if err == nil {
		err = errors.New("the host presented no key")
	}
	return nil, err
}

type perMinuteLimiter struct {
	mu   sync.Mutex
	seen map[string]*perMinuteWindow
}

type perMinuteWindow struct {
	start time.Time
	calls int
}

func (l *perMinuteLimiter) allow(key string, perMin int, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen == nil {
		l.seen = map[string]*perMinuteWindow{}
	}
	for k, w := range l.seen {
		if now.Sub(w.start) > time.Minute {
			delete(l.seen, k)
		}
	}
	w := l.seen[key]
	if w == nil {
		w = &perMinuteWindow{start: now}
		l.seen[key] = w
	}
	w.calls++
	return w.calls <= perMin
}
