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
	Host string `json:"host"`
	Kind string `json:"kind"`
	// Port is the ssh port the host key is read from; 22 when empty.
	Port       int    `json:"port,omitempty"`
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
	gitCredentialsPerMin  = 10
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

func (s *Server) boundCipher(w http.ResponseWriter) (BoundCipher, bool) {
	bc, ok := s.secretsCipher.(BoundCipher)
	if !ok || s.secretsCipher == nil {
		writeAuthError(w, http.StatusConflict, authErrorBody{
			Code: "secrets_key_required",
			Message: "this controller stores no secret sealed, so it holds no git credential; " +
				"start it with SPARKWING_SECRETS_KEY",
		})
		return nil, false
	}
	return bc, true
}

func (s *Server) handleListGitCredentials(w http.ResponseWriter, r *http.Request) {
	noStoreSecrets(w)
	_, t, ok := s.teamMember(w, r, store.RoleReader)
	if !ok {
		return
	}
	creds, err := t.GitCredentials(r.Context())
	if err != nil {
		s.writeInternalError(w, r, "list git credentials", err)
		return
	}
	out := make([]GitCredentialJSON, 0, len(creds))
	for _, c := range creds {
		out = append(out, gitCredentialJSON(c, ""))
	}
	writeJSON(w, http.StatusOK, map[string]any{"credentials": out})
}

// handlePutGitCredential stores, or replaces, the team's credential for a
// host. An ssh key is stored with the host key the controller reads from
// the host now, and is released only after the owner confirms that key's
// fingerprint.
func (s *Server) handlePutGitCredential(w http.ResponseWriter, r *http.Request) {
	noStoreSecrets(w)
	p, t, ok := s.teamMember(w, r, store.RoleOwner)
	if !ok {
		return
	}
	cipher, ok := s.boundCipher(w)
	if !ok {
		return
	}
	var req gitCredentialPutReq
	if err := decodeJSONLimit(r, &req, maxSecretJSONBody); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	host := strings.ToLower(strings.TrimSpace(req.Host))
	if !validGitCredentialHost(host) {
		writeError(w, http.StatusBadRequest, errors.New("host is a hostname such as gitlab.com, with no scheme, port or path"))
		return
	}
	cred := store.GitCredential{Host: host, Kind: req.Kind, CreatedBy: p.AccountID}
	var secret, publicKey string
	switch req.Kind {
	case store.GitCredentialSSH:
		signer, err := parseDeployKey(req.PrivateKey)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		port := req.Port
		if port == 0 {
			port = 22
		}
		if port < 1 || port > 65535 {
			writeError(w, http.StatusBadRequest, errors.New("port is an ssh port between 1 and 65535"))
			return
		}
		key, err := s.scanHostKey(r.Context(), host, port)
		if err != nil {
			s.logger.Info("git credential host key scan failed", "team", string(p.Team), "host", host, "err", err.Error())
			writeError(w, http.StatusBadGateway, fmt.Errorf("read the ssh host key of %s: %w", host, err))
			return
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
			writeError(w, http.StatusBadRequest, errors.New("username is up to 128 printable characters with no space"))
			return
		}
		if !credentialProtocolValue(req.Token) {
			writeError(w, http.StatusBadRequest, errors.New("token is up to 1024 printable characters with no space"))
			return
		}
		secret = req.Token
	default:
		writeError(w, http.StatusBadRequest, errors.New("kind is ssh or https"))
		return
	}
	sealed, err := sealSecret(cipher, gitCredentialBinding(t.Team(), host), secret)
	if err != nil {
		s.writeInternalError(w, r, "seal git credential", err)
		return
	}
	cred.Secret = sealed
	stored, err := t.PutGitCredential(r.Context(), cred, time.Now())
	if errors.Is(err, store.ErrGitCredentialLimit) {
		writeError(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		s.writeInternalError(w, r, "store git credential", err)
		return
	}
	s.logger.Info("git credential stored", "team", string(p.Team), "host", host, "kind", stored.Kind,
		"fingerprint", stored.Fingerprint, "confirmed", stored.ConfirmedAt != nil, "by", p.AccountID)
	writeJSON(w, http.StatusOK, gitCredentialJSON(stored, publicKey))
}

func (s *Server) handleConfirmGitCredential(w http.ResponseWriter, r *http.Request) {
	noStoreSecrets(w)
	p, t, ok := s.teamMember(w, r, store.RoleOwner)
	if !ok {
		return
	}
	var req gitCredentialConfirmReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	host := r.PathValue("host")
	c, err := t.ConfirmGitCredential(r.Context(), host, strings.TrimSpace(req.Fingerprint), time.Now())
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, err)
		return
	case errors.Is(err, store.ErrFingerprintMismatch):
		writeError(w, http.StatusConflict, err)
		return
	case err != nil:
		s.writeInternalError(w, r, "confirm git credential", err)
		return
	}
	s.logger.Info("git credential confirmed", "team", string(p.Team), "host", host,
		"fingerprint", c.Fingerprint, "by", p.AccountID)
	writeJSON(w, http.StatusOK, gitCredentialJSON(c, ""))
}

func (s *Server) handleDeleteGitCredential(w http.ResponseWriter, r *http.Request) {
	noStoreSecrets(w)
	p, t, ok := s.teamMember(w, r, store.RoleOwner)
	if !ok {
		return
	}
	host := r.PathValue("host")
	if err := t.DeleteGitCredential(r.Context(), host); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		s.writeInternalError(w, r, "delete git credential", err)
		return
	}
	s.logger.Info("git credential deleted", "team", string(p.Team), "host", host, "by", p.AccountID)
	w.WriteHeader(http.StatusNoContent)
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

type runnerGitCredentialsReq struct {
	Enabled bool `json:"enabled"`
}

// handleSetRunnerGitCredentials opts one of the team's runner tokens in to,
// or out of, receiving the team's git credentials. A cloud runner receives
// them without it.
func (s *Server) handleSetRunnerGitCredentials(w http.ResponseWriter, r *http.Request) {
	p, t, ok := s.teamMember(w, r, store.RoleOwner)
	if !ok {
		return
	}
	var req runnerGitCredentialsReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	prefix := r.PathValue("prefix")
	err := t.SetGitCredentialMachine(r.Context(), prefix, req.Enabled, p.AccountID, time.Now())
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		s.writeInternalError(w, r, "set runner git credentials", err)
		return
	}
	s.logger.Info("runner git credentials set", "team", string(p.Team), "prefix", prefix,
		"enabled", req.Enabled, "by", p.AccountID)
	writeJSON(w, http.StatusOK, req)
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

// credentialProtocolValue is a username or token that fits one line of
// git's credential protocol.
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

// scanHostKey reads host's ssh host key the way ssh-keyscan does: it opens
// an ssh handshake, keeps the key the server presents, and hangs up before
// authenticating.
//
// safety: an owner names the host, so it is held to the runner's own rule
// for where a fetch may go, and the controller never probes its own network.
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

// readHostKey runs the handshake on conn, which it closes, as the ssh client
// of addr.
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

// claimRateLimiter counts one claim's asks per minute.
type claimRateLimiter struct {
	mu   sync.Mutex
	seen map[string]*claimRateWindow
}

type claimRateWindow struct {
	start time.Time
	calls int
}

// allow reports whether key may ask again, at most perMin times a minute.
func (l *claimRateLimiter) allow(key string, perMin int, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen == nil {
		l.seen = map[string]*claimRateWindow{}
	}
	for k, w := range l.seen {
		if now.Sub(w.start) > time.Minute {
			delete(l.seen, k)
		}
	}
	w := l.seen[key]
	if w == nil {
		w = &claimRateWindow{start: now}
		l.seen[key] = w
	}
	w.calls++
	return w.calls <= perMin
}
