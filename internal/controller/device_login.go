package controller

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"time"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

const (
	deviceLoginTTL      = 10 * time.Minute
	deviceLoginInterval = 5 * time.Second
	deviceCodePrefix    = "swd_"
	deviceVerification  = "/device"
	// safety: twenty consonants, no vowels or look-alikes, so a code reads unambiguously and spells no word;
	// eight of them give about 2^34 codes against a per-account limit on browser guesses.
	userCodeAlphabet = "BCDFGHJKLMNPQRSTVWXZ"
	userCodeLength   = 8
	// safety: every device route is unauthenticated or takes a short code, and start stores its body.
	deviceBodyLimit = 16 << 10
	maxDeviceRepo   = 256
	// safety: an administration token reads every secret and can invite members, so a leaked copy lasts an hour.
	adminStepUpLifetime = time.Hour
)

// safety: these match the console's Create CLI token; team.admin travels only with an owner's own step-up.
var cliMemberGrants = []capability.Grant{capability.RunsRead, capability.LogsRead, capability.RunsWrite, capability.RunsControl, capability.ApprovalsWrite}

// safety: Runner reuses the enrollment body, so a device enrollment is validated and issued as POST /api/v1/runners.
// Account and the Starter fields are set only by the step-up route, from the bearer that asked; a body carrying
// them is refused. The starter's grants and expiry bound the member token a step-up re-issues.
type deviceLoginRequest struct {
	Kind             string                   `json:"kind"`
	Machine          string                   `json:"machine"`
	Team             string                   `json:"team,omitempty"`
	Grants           []capability.Grant       `json:"grants,omitempty"`
	Runner           *runnerEnrollmentRequest `json:"runner,omitempty"`
	Account          string                   `json:"account,omitempty"`
	StarterGrants    []capability.Grant       `json:"starter_grants,omitempty"`
	StarterExpiresAt *time.Time               `json:"starter_expires_at,omitempty"`
}

func (request deviceLoginRequest) claimsStepUpFacts() bool {
	return request.Account != "" || len(request.StarterGrants) != 0 || request.StarterExpiresAt != nil
}

func (s *Server) deviceLoginEndpoints() []controllerwire.Endpoint {
	return []controllerwire.Endpoint{
		{Method: "POST", Pattern: "/api/v1/auth/device", Authentication: capability.AuthPublic, Handle: s.handleStartDeviceLogin},
		{Method: "POST", Pattern: "/api/v1/auth/device/step-up", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleStartStepUp},
		{Method: "POST", Pattern: "/api/v1/auth/device/token", Authentication: capability.AuthPublic, Handle: s.handlePollDeviceLogin},
		{Method: "POST", Pattern: "/api/v1/auth/device/cancel", Authentication: capability.AuthPublic, Handle: s.handleCancelDeviceLogin},
		{Method: "POST", Pattern: "/api/v1/auth/device/lookup", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleLookupDeviceLogin},
		{Method: "POST", Pattern: "/api/v1/auth/device/approve", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleDecideDeviceLogin(true)},
		{Method: "POST", Pattern: "/api/v1/auth/device/deny", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleDecideDeviceLogin(false)},
	}
}

func (s *Server) handleStartDeviceLogin(w http.ResponseWriter, r *http.Request, _ capability.Authorized) error {
	if err := s.loginLimit.admit(r); err != nil {
		return err
	}
	if err := s.deviceLoginAvailable(); err != nil {
		return err
	}
	r.Body = http.MaxBytesReader(nil, r.Body, deviceBodyLimit)
	var request deviceLoginRequest
	if err := decodeEngineRequest(r, &request); err != nil {
		return err
	}
	if request.Kind == controllerwire.DeviceLoginAdmin || request.claimsStepUpFacts() {
		return invalidEngineBody("administration is requested from a signed-in terminal; run sparkwing login --admin")
	}
	return s.createDeviceLogin(w, r, request)
}

// safety: the request records the bearer's account and team, so only the same person can approve it.
func (s *Server) handleStartStepUp(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	if err := s.loginLimit.admit(r); err != nil {
		return err
	}
	if err := s.deviceLoginAvailable(); err != nil {
		return err
	}
	p := a.Principal
	if p.Class != capability.CredentialBearer || p.Capability.Subject.Kind != capability.SubjectAccount {
		return hosterr.Authorization(hosterr.ReasonBinding, "administration is requested from a terminal signed in with sparkwing login; run sparkwing login --admin there")
	}
	r.Body = http.MaxBytesReader(nil, r.Body, deviceBodyLimit)
	var request deviceLoginRequest
	if err := decodeEngineRequest(r, &request); err != nil {
		return err
	}
	if request.Kind != controllerwire.DeviceLoginAdmin || request.claimsStepUpFacts() || request.Team != "" && request.Team != a.Target.Team {
		return invalidEngineBody("a step-up asks for kind admin with a machine name; the team and account come from the signed-in token")
	}
	// safety: the re-issued member token keeps the starter's grants and expiry, so stepping up never widens it.
	starter := slices.DeleteFunc(slices.Clone(cliMemberGrants), func(grant capability.Grant) bool {
		return !slices.Contains(p.Capability.Grants, grant)
	})
	request.Account, request.Team, request.StarterGrants, request.StarterExpiresAt = p.Capability.Subject.ID, a.Target.Team, starter, p.Capability.ExpiresAt
	return s.createDeviceLogin(w, r, request)
}

func (s *Server) createDeviceLogin(w http.ResponseWriter, r *http.Request, request deviceLoginRequest) error {
	if err := validateDeviceLoginRequest(&request); err != nil {
		return err
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return err
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return err
	}
	deviceCode := deviceCodePrefix + base64.RawURLEncoding.EncodeToString(raw[:])
	// safety: a fresh user code can collide with a live one; three draws make a repeat collision vanishingly rare.
	for range 3 {
		userCode, err := newUserCode()
		if err != nil {
			return err
		}
		_, err = s.operator.CreateDeviceLogin(r.Context(), sha256.Sum256([]byte(deviceCode)), userCode, encoded, deviceLoginTTL, deviceLoginInterval)
		if errors.Is(err, store.ErrDeviceCodeTaken) {
			continue
		}
		if err != nil {
			return engineLookupError(err)
		}
		noteAuditWrite(r.Context())
		writeJSON(w, http.StatusCreated, controllerwire.DeviceLoginStarted{
			DeviceCode: deviceCode, UserCode: userCode, VerificationPath: deviceVerification,
			ExpiresIn: int64(deviceLoginTTL / time.Second), Interval: int64(deviceLoginInterval / time.Second),
		})
		return nil
	}
	return &apierr.Error{Code: hosterr.Unavailable, Message: "could not choose a free device code; try again"}
}

func (s *Server) handlePollDeviceLogin(w http.ResponseWriter, r *http.Request, _ capability.Authorized) error {
	if err := s.deviceLoginAvailable(); err != nil {
		return err
	}
	digest, err := decodeDeviceCode(r)
	if err != nil {
		return err
	}
	poll, err := s.operator.PollDeviceLogin(r.Context(), digest)
	if err != nil {
		return devicePollError(err)
	}
	if poll.Status != store.DeviceApproved {
		writeJSON(w, http.StatusOK, controllerwire.DeviceLoginPoll{Status: string(poll.Status), Interval: int64(poll.Interval / time.Second)})
		return nil
	}
	noteAuditWrite(r.Context())
	noteAuditTarget(r.Context(), "device_login", poll.Login.ID)
	now := time.Now().UTC()
	spec, err := deviceLoginSpec(poll.Login, now)
	if err != nil {
		return err
	}
	out := controllerwire.DeviceLoginPoll{Status: controllerwire.DeviceApproved}
	// safety: a fresh sign-in ends the browser's earlier session and the member token it approved, so an
	// administration approval also re-issues the terminal's member token under the session that approved it.
	if spec.AllowTeamAdmin {
		var request deviceLoginRequest
		if err := json.Unmarshal(poll.Login.Request, &request); err != nil {
			return err
		}
		member := spec
		member.AllowTeamAdmin, member.ExpiresAt = false, request.StarterExpiresAt
		member.Grants = slices.DeleteFunc(slices.Clone(spec.Grants), func(grant capability.Grant) bool { return grant == capability.TeamAdmin })
		issued, err := s.issueRoot(r.Context(), member)
		if err != nil {
			return err
		}
		out.Member = createdToken(issued)
	}
	issued, err := s.issueRoot(r.Context(), spec)
	if err != nil {
		return err
	}
	out.Credential = createdToken(issued)
	writeJSON(w, http.StatusOK, out)
	return nil
}

func createdToken(issued credentialResponse) *controllerwire.CreatedToken {
	credential := &controllerwire.CreatedToken{Principal: issued.Principal, Capability: issued.Capability, Prefix: issued.Prefix, Team: issued.Team, ExpiresAt: issued.ExpiresAt}
	for _, grant := range issued.Grants {
		credential.Grants = append(credential.Grants, string(grant))
	}
	return credential
}

func (s *Server) handleCancelDeviceLogin(w http.ResponseWriter, r *http.Request, _ capability.Authorized) error {
	if err := s.deviceLoginAvailable(); err != nil {
		return err
	}
	digest, err := decodeDeviceCode(r)
	if err != nil {
		return err
	}
	if err := s.operator.CancelDeviceLogin(r.Context(), digest); err != nil {
		return devicePollError(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) handleLookupDeviceLogin(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	login, request, err := s.pendingDeviceLogin(r, a)
	if err != nil {
		return err
	}
	if err := deviceTeamMatches(request, a); err != nil {
		return err
	}
	grants, err := deviceLoginGrants(request, a.Principal)
	if err != nil {
		return err
	}
	out := controllerwire.DeviceLoginLookup{
		UserCode: login.UserCode, Kind: request.Kind, Machine: request.Machine, Team: a.Target.Team,
		CreatedAt: login.CreatedAt, ExpiresAt: login.ExpiresAt,
		SignInRequired: request.Kind == controllerwire.DeviceLoginAdmin && !recentSignIn(a.Principal.Capability.IssuedAt, time.Now()),
	}
	for _, grant := range grants {
		out.Grants = append(out.Grants, string(grant))
	}
	if runner := request.Runner; runner != nil {
		out.Runner = &controllerwire.DeviceRunner{
			AllowedLabels: match.SelfAsserted(runner.AllowedLabels),
			Name:          runner.Name, Personal: runner.Personal, Repos: append([]string{}, runner.Repos...), ReservedPools: append([]string{}, runner.ReservedPools...),
			Trust: string(runner.Trust), Platform: runner.Platform, Launcher: slices.Contains(grants, capability.ClaimsLaunch),
		}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) handleDecideDeviceLogin(approve bool) func(http.ResponseWriter, *http.Request, capability.Authorized) error {
	return func(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
		login, request, err := s.pendingDeviceLogin(r, a)
		if err != nil {
			return err
		}
		var grants []capability.Grant
		var spec *capability.ProvisionSpec
		if approve {
			if err := deviceTeamMatches(request, a); err != nil {
				return err
			}
			if grants, err = deviceLoginGrants(request, a.Principal); err != nil {
				return err
			}
			now := time.Now()
			if request.Kind == controllerwire.DeviceLoginAdmin && !recentSignIn(a.Principal.Capability.IssuedAt, now) {
				return hosterr.Authorization(hosterr.ReasonReauthRequired, "approving administration needs a sign-in from the last 10 minutes; sign out, sign in again and enter the code again")
			}
			issued, err := deviceLoginSpec(store.DeviceLogin{
				Request: login.Request, Team: store.Team(a.Target.Team), AccountID: a.Principal.Capability.Subject.ID, Session: a.Principal.Authority, Grants: grants,
			}, now.UTC())
			if err != nil {
				return err
			}
			spec = &issued
		}
		decided, err := s.operator.DecideDeviceLogin(r.Context(), a.Principal, login.UserCode, grants, spec)
		if err != nil {
			return deviceLookupError(err)
		}
		noteAuditTarget(r.Context(), "device_login", decided.ID)
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
}

func (s *Server) pendingDeviceLogin(r *http.Request, a capability.Authorized) (store.DeviceLogin, deviceLoginRequest, error) {
	if err := s.deviceLoginAvailable(); err != nil {
		return store.DeviceLogin{}, deviceLoginRequest{}, err
	}
	if err := s.loginLimit.admitAccount(a.Principal.Capability.Subject.ID); err != nil {
		return store.DeviceLogin{}, deviceLoginRequest{}, err
	}
	r.Body = http.MaxBytesReader(nil, r.Body, deviceBodyLimit)
	var body controllerwire.UserCodeRequest
	if err := decodeEngineRequest(r, &body); err != nil {
		return store.DeviceLogin{}, deviceLoginRequest{}, err
	}
	code, ok := normalizeUserCode(body.UserCode)
	if !ok {
		return store.DeviceLogin{}, deviceLoginRequest{}, invalidEngineBody("user_code must be the 8 letters your terminal shows")
	}
	login, err := s.operator.PendingDeviceLogin(r.Context(), code)
	if err != nil {
		return store.DeviceLogin{}, deviceLoginRequest{}, deviceLookupError(err)
	}
	var request deviceLoginRequest
	if err := json.Unmarshal(login.Request, &request); err != nil {
		return store.DeviceLogin{}, deviceLoginRequest{}, err
	}
	return login, request, nil
}

func (s *Server) deviceLoginAvailable() error {
	if s.operator == nil {
		return routepolicy.MissingEndpointPolicy()
	}
	if !s.deployment.MultiTeam() {
		return &apierr.Error{Code: hosterr.CapabilityUnsupported, Message: "a local controller has no account sign-in; the CLI reaches it over its socket without a token"}
	}
	return nil
}

func validateDeviceLoginRequest(request *deviceLoginRequest) error {
	switch request.Kind {
	case controllerwire.DeviceLoginMember:
		if request.Runner != nil || request.claimsStepUpFacts() {
			return invalidEngineBody("a member device login carries no runner")
		}
		seen := map[capability.Grant]bool{}
		for _, grant := range request.Grants {
			if seen[grant] || !slices.Contains(cliMemberGrants, grant) {
				return invalidEngineBody("grants must be unique and among runs.read, logs.read, runs.write, runs.control and approvals.write")
			}
			seen[grant] = true
		}
	case controllerwire.DeviceLoginAdmin:
		if request.Runner != nil || len(request.Grants) != 0 || request.Account == "" || request.Team == "" || len(request.StarterGrants) == 0 {
			return invalidEngineBody("an administration request carries a machine name only; its grants follow the approver's role")
		}
	case controllerwire.DeviceLoginRunner:
		if request.Runner == nil || len(request.Grants) != 0 || request.claimsStepUpFacts() {
			return invalidEngineBody("a runner device login carries its enrollment in runner and no member grants")
		}
		if len(request.Runner.AllowedLabels) != 0 {
			return invalidEngineBody("browser approval does not accept allowed_labels; have an administrator run sparkwing runners add --runner NAME --personal --label LABEL --session-stdin with an admin session token on stdin")
		}
		// safety: a browser approval is phishable, so it never grants a credential that runs other members' work.
		if runner := request.Runner; !runner.Personal || len(runner.ReservedPools) != 0 || runner.Trust != "" || runner.Platform != "" || runner.Grants != nil {
			return invalidEngineBody("browser approval enrolls personal runners only; enroll shared runners, reserved pools, trust classes, platforms and launchers with --session-stdin")
		}
		for _, repo := range request.Runner.Repos {
			if len(repo) > maxDeviceRepo {
				return invalidEngineBody("repos patterns must be at most 256 characters")
			}
		}
		if _, err := runnerEnrollmentSpec(*request.Runner, capability.AuthorityRef{}, ""); err != nil {
			return err
		}
		request.Machine = request.Runner.Name
	default:
		return invalidEngineBody("kind must be member or runner")
	}
	if !store.ValidRunnerName(request.Machine) {
		return invalidEngineBody("machine must be 1 to 63 letters, digits, '-', '_' or '.'")
	}
	if request.Team != "" && (len(request.Team) > 63 || store.NormalizeTeam(store.Team(request.Team)) != store.Team(request.Team)) {
		return invalidEngineBody("team must be a team slug")
	}
	return nil
}

// safety: these are the exact grants shown and issued; an empty member request takes the approver's run and log
// grants, as the console's CLI token does.
func deviceLoginGrants(request deviceLoginRequest, approver capability.Principal) ([]capability.Grant, error) {
	if request.Kind == controllerwire.DeviceLoginRunner {
		return runnerGrants(request.Runner.Grants)
	}
	if request.Kind == controllerwire.DeviceLoginAdmin {
		// safety: a code typed by anyone but the account whose terminal asked could be a phished one.
		if request.Account != approver.Capability.Subject.ID {
			return nil, hosterr.Authorization(hosterr.ReasonBinding, "this code asks for another account's administration; deny it")
		}
		if !slices.Contains(approver.Capability.Grants, capability.TeamAdmin) {
			return nil, hosterr.Authorization(hosterr.ReasonMissingGrant, "only team owners can approve administration; deny this request")
		}
		return append(slices.Clone(request.StarterGrants), capability.TeamAdmin), nil
	}
	if len(request.Grants) != 0 {
		return slices.Clone(request.Grants), nil
	}
	return slices.DeleteFunc(slices.Clone(cliMemberGrants), func(grant capability.Grant) bool {
		return !slices.Contains(approver.Capability.Grants, grant)
	}), nil
}

func deviceTeamMatches(request deviceLoginRequest, a capability.Authorized) error {
	if request.Team != "" && request.Team != a.Target.Team {
		return hosterr.Authorization(hosterr.ReasonBinding, "this device asked to join team "+request.Team+"; switch to that team to approve it, or deny it")
	}
	return nil
}

func deviceLoginSpec(login store.DeviceLogin, now time.Time) (capability.ProvisionSpec, error) {
	var request deviceLoginRequest
	if err := json.Unmarshal(login.Request, &request); err != nil {
		return capability.ProvisionSpec{}, err
	}
	if request.Kind == controllerwire.DeviceLoginRunner {
		return runnerEnrollmentSpec(*request.Runner, login.Session, string(login.Team))
	}
	spec := capability.ProvisionSpec{
		Kind: capability.ProvisionMember, Authority: &login.Session, Subject: capability.Subject{Kind: capability.SubjectAccount, ID: login.AccountID},
		Team: string(login.Team), Grants: login.Grants, Binding: capability.Binding{Kind: capability.BindingNone},
	}
	if request.Kind == controllerwire.DeviceLoginAdmin {
		expires := now.Add(adminStepUpLifetime)
		spec.AllowTeamAdmin, spec.ExpiresAt = true, &expires
	}
	return spec, nil
}

func decodeDeviceCode(r *http.Request) ([32]byte, error) {
	r.Body = http.MaxBytesReader(nil, r.Body, deviceBodyLimit)
	var body controllerwire.DeviceCodeRequest
	if err := decodeEngineRequest(r, &body); err != nil {
		return [32]byte{}, err
	}
	if !strings.HasPrefix(body.DeviceCode, deviceCodePrefix) || len(body.DeviceCode) > 128 {
		return [32]byte{}, deviceRefusal(controllerwire.DeviceInvalidGrant, "the device code is unknown or already used; run the command again")
	}
	return sha256.Sum256([]byte(body.DeviceCode)), nil
}

func newUserCode() (string, error) {
	var b strings.Builder
	limit := big.NewInt(int64(len(userCodeAlphabet)))
	for range userCodeLength {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", err
		}
		b.WriteByte(userCodeAlphabet[n.Int64()])
	}
	return b.String(), nil
}

func normalizeUserCode(raw string) (string, bool) {
	var b strings.Builder
	for _, r := range strings.ToUpper(raw) {
		switch {
		case r == '-' || r == ' ':
		case strings.ContainsRune(userCodeAlphabet, r):
			b.WriteRune(r)
		default:
			return "", false
		}
	}
	return b.String(), b.Len() == userCodeLength
}

func deviceRefusal(reason, message string) error {
	return &apierr.Error{Code: apierr.BadRequest, Message: message, Details: map[string]any{"reason": reason}}
}

func devicePollError(err error) error {
	switch {
	case errors.Is(err, store.ErrDeviceLoginDenied):
		return deviceRefusal(controllerwire.DeviceDenied, "the sign-in was denied in the browser or cancelled")
	case errors.Is(err, store.ErrDeviceLoginExpired):
		return deviceRefusal(controllerwire.DeviceExpired, "the device code expired before it was approved; run the command again")
	case errors.Is(err, store.ErrNotFound):
		return deviceRefusal(controllerwire.DeviceInvalidGrant, "the device code is unknown or already used; run the command again")
	}
	return engineLookupError(err)
}

func deviceLookupError(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return &apierr.Error{Code: apierr.NotFound, Message: "that code is unknown, expired or already used; run the command again in your terminal for a new code"}
	}
	return engineLookupError(err)
}
