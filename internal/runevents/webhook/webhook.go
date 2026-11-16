// Package webhook sends one signed run event delivery attempt and classifies
// its outcome. It owns the request shape, Standard Webhooks signing, the
// destination policy and the retry schedule; the caller owns persistence.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

// Safe outcome codes recorded as a delivery's last error. They never carry
// receiver-supplied text.
const (
	CodeHTTPStatus = "http_status"
	CodeTimeout    = "timeout"
	CodeDNS        = "dns"
	CodeRefused    = "refused"
	CodeTLS        = "tls"
	CodePolicy     = "policy"
	CodeSecret     = "secret"
	CodeTransport  = "transport"
)

const (
	// AttemptTimeout bounds one attempt from dial to response headers.
	AttemptTimeout = 10 * time.Second
	// MaxDelay caps both the backoff schedule and how far a Retry-After can lengthen a wait.
	MaxDelay     = time.Hour
	contentType  = "application/cloudevents+json"
	secretPrefix = "whsec_"
)

// safety: indexed by the 1-based attempt that just failed; later attempts reuse the last entry.
var schedule = []time.Duration{10 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute, time.Hour}

// Sign returns the Standard Webhooks `webhook-signature` value for one attempt:
// v1,<base64 HMAC-SHA256(key, id "." unix-seconds "." body)>, where key is the
// base64-decoded part of a whsec_ secret.
func Sign(secret, id string, at time.Time, body []byte) (string, error) {
	key, err := signingKey(secret)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id + "." + strconv.FormatInt(at.Unix(), 10) + "."))
	mac.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil)), nil
}

func signingKey(secret string) ([]byte, error) {
	encoded, ok := strings.CutPrefix(secret, secretPrefix)
	if !ok {
		return nil, errors.New("signing secret has no whsec_ prefix")
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) == 0 {
		return nil, errors.New("signing secret is not base64")
	}
	return key, nil
}

// Policy decides which destinations a sender may reach.
type Policy struct {
	// Local permits http and any address, loopback included. Otherwise only
	// https to public addresses is allowed.
	Local bool
	// Lookup resolves host names; nil uses net.DefaultResolver.
	Lookup sourceurl.Lookup
	// Dial opens the connection to an already-checked address; nil uses net.Dialer.
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
}

// Attempt is one delivery send.
type Attempt struct {
	URL       string
	EventID   string
	Body      []byte
	Secret    string
	UserAgent string
	At        time.Time
}

// Result classifies an attempt. Delivered is true only for a 2xx response.
type Result struct {
	Delivered bool
	Status    int
	Code      string
	// RetryAfter is the receiver's valid Retry-After, clamped to [0, MaxDelay].
	RetryAfter *time.Duration
}

// Sender sends attempts under one destination policy.
type Sender struct {
	policy Policy
	client *http.Client
}

// NewSender returns a sender that never follows redirects, uses no proxy or
// cookies, and dials only addresses the policy checked.
func NewSender(policy Policy) *Sender {
	if policy.Lookup == nil {
		policy.Lookup = net.DefaultResolver.LookupIPAddr
	}
	if policy.Dial == nil {
		dialer := &net.Dialer{}
		policy.Dial = dialer.DialContext
	}
	s := &Sender{policy: policy}
	s.client = &http.Client{
		Transport: &http.Transport{
			Proxy:             nil,
			DialContext:       s.dial,
			DisableKeepAlives: true,
			ForceAttemptHTTP2: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return s
}

type policyError struct{ reason string }

func (e policyError) Error() string { return "destination refused: " + e.reason }

// CheckURL applies the static part of the policy to a subscription URL.
func (p Policy) CheckURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" || u.Hostname() == "" {
		return nil, policyError{"URL is not absolute"}
	}
	if u.User != nil || u.Fragment != "" || u.RawFragment != "" {
		return nil, policyError{"URL carries userinfo or a fragment"}
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && p.Local:
	default:
		return nil, policyError{"scheme " + u.Scheme + " is not allowed"}
	}
	if !p.Local {
		if err := sourceurl.CheckPublicHost(u.Hostname()); err != nil {
			return nil, policyError{err.Error()}
		}
	}
	return u, nil
}

// safety: the address is resolved and checked here, then dialed directly, so
// a second resolution cannot swap in an address the check never saw.
func (s *Sender) dial(ctx context.Context, network, address string) (net.Conn, error) {
	if s.policy.Local {
		return s.policy.Dial(ctx, network, address)
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	var addrs []net.IP
	if ip := net.ParseIP(host); ip != nil {
		addrs = []net.IP{ip}
	} else {
		found, err := s.policy.Lookup(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, a := range found {
			addrs = append(addrs, a.IP)
		}
	}
	if len(addrs) == 0 {
		return nil, &net.DNSError{Err: "no addresses", Name: host, IsNotFound: true}
	}
	for _, ip := range addrs {
		if !sourceurl.PublicIP(ip) {
			return nil, policyError{host + " resolves to a non-public address"}
		}
	}
	var errs []error
	for _, ip := range addrs {
		conn, err := s.policy.Dial(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		errs = append(errs, err)
	}
	return nil, errors.Join(errs...)
}

// Send performs one attempt. It never returns receiver-supplied text.
func (s *Sender) Send(ctx context.Context, a Attempt) Result {
	u, err := s.policy.CheckURL(a.URL)
	if err != nil {
		return Result{Code: CodePolicy}
	}
	signature, err := Sign(a.Secret, a.EventID, a.At, a.Body)
	if err != nil {
		return Result{Code: CodeSecret}
	}
	ctx, cancel := context.WithTimeout(ctx, AttemptTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(a.Body))
	if err != nil {
		return Result{Code: CodePolicy}
	}
	req.Header = http.Header{
		"Content-Type":      {contentType},
		"User-Agent":        {a.UserAgent},
		"Webhook-Id":        {a.EventID},
		"Webhook-Timestamp": {strconv.FormatInt(a.At.Unix(), 10)},
		"Webhook-Signature": {signature},
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return Result{Code: classify(err)}
	}
	// safety: keep-alives are off, so the receiver's body is never read; closing ends the connection.
	_ = resp.Body.Close()
	result := Result{Status: resp.StatusCode, Delivered: resp.StatusCode >= 200 && resp.StatusCode < 300}
	if !result.Delivered {
		result.Code = CodeHTTPStatus
		result.RetryAfter = retryAfter(resp.Header.Get("Retry-After"), a.At)
	}
	return result
}

func classify(err error) string {
	var pe policyError
	var dnsErr *net.DNSError
	var netErr net.Error
	var certErr *tls.CertificateVerificationError
	var unknownAuthority x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var recordErr tls.RecordHeaderError
	switch {
	case errors.As(err, &pe):
		return CodePolicy
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return CodeTimeout
	case errors.As(err, &dnsErr):
		return CodeDNS
	case errors.Is(err, syscall.ECONNREFUSED):
		return CodeRefused
	case errors.As(err, &certErr), errors.As(err, &unknownAuthority), errors.As(err, &hostErr), errors.As(err, &recordErr):
		return CodeTLS
	}
	return CodeTransport
}

func retryAfter(value string, now time.Time) *time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	var d time.Duration
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds < 0 {
			return nil
		}
		d = MaxDelay
		if seconds < int64(MaxDelay/time.Second) {
			d = time.Duration(seconds) * time.Second
		}
	} else if at, err := http.ParseTime(value); err == nil {
		d = max(at.Sub(now), 0)
	} else {
		return nil
	}
	d = min(d, MaxDelay)
	return &d
}

// Delay returns how long to wait after the given failed attempt (1-based):
// uniform in [base/2, base] over the schedule, so a failing receiver is never
// retried at once. A Retry-After can lengthen that wait up to MaxDelay but never
// shorten it. jitter returns a value in [0, n).
func Delay(attempt int64, r Result, jitter func(n int64) int64) time.Duration {
	base := schedule[len(schedule)-1]
	if attempt >= 1 && attempt <= int64(len(schedule)) {
		base = schedule[attempt-1]
	}
	half := base / 2
	wait := half + time.Duration(jitter(int64(half)+1))
	if r.RetryAfter != nil {
		wait = max(wait, *r.RetryAfter)
	}
	return wait
}

// UserAgent is the fixed User-Agent for a Sparkwing build version.
func UserAgent(version string) string {
	if version == "" {
		version = "dev"
	}
	return fmt.Sprintf("Sparkwing-Webhooks/%s", version)
}
